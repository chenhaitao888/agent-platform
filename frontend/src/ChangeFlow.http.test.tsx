import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { ChangeRequest } from './change'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_CHANGE_URL
afterEach(() => vi.unstubAllGlobals())
it.skipIf(!endpoint)('freezes a three-service requirement through the deployed authenticated API', async () => {
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  let loseSnapshotResponse = true
  const snapshotBodies: string[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (path.endsWith('/snapshots')) {
      snapshotBodies.push(init!.body as string)
      if (loseSnapshotResponse && response.ok) {
        loseSnapshotResponse = false
        await response.arrayBuffer()
        throw new TypeError('controlled lost snapshot response')
      }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无需求')
  fireEvent.change(screen.getByLabelText('需求名称或编号'), { target: { value: '多服务需求 A' } })
  for (const id of ['S1', 'S2', 'S3']) {
    fireEvent.click(screen.getByLabelText(`选择 ${id}`))
    fireEvent.change(screen.getByLabelText(`${id} 源分支`), { target: { value: 'feature/A' } })
  }
  fireEvent.click(screen.getByRole('button', { name: '创建需求' }))
  await screen.findByText('尚未固定快照')
  expect(snapshotBodies).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '固定代码快照' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('快照结果未确认')
  fireEvent.click(screen.getByRole('button', { name: '重试本次快照' }))
  await screen.findByText('快照已固定 · 待审阅')
  expect(snapshotBodies[0]).toBe(snapshotBodies[1])
  fireEvent.change(screen.getByLabelText('快照 S1 源分支'), { target: { value: 'feature/new' } })
  fireEvent.click(screen.getByRole('button', { name: '创建新快照' }))
  await screen.findByRole('heading', { name: '快照 2' })
  expect(JSON.parse(snapshotBodies[2]).idempotencyKey).not.toBe(JSON.parse(snapshotBodies[1]).idempotencyKey)
  const headers = { Authorization: `Bearer ${token}` }
  const record = await (await originalFetch(`${endpoint}/api/v1/changes/change-1`, { headers })).json() as ChangeRequest
  expect(record.version).toBe(3)
  expect(record.snapshots).toHaveLength(2)
  expect(record.currentSnapshotId).toBe('snapshot-2')
  expect(record.snapshots[0].services.map(s => s.targetBranch)).toEqual(['main', 'release/test', 'master'])
  expect(record.snapshots[0].services[0].repository?.headSha).toBe('a'.repeat(40))
  expect(record.snapshots[1].services[0].repository?.headSha).toBe('d'.repeat(40))
  expect(record.snapshots[0].services.every(s => s.repository?.baseSha === 'c'.repeat(40))).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: '重新读取详情' }))
  await screen.findByRole('heading', { name: '快照 2' })
  expect(snapshotBodies).toHaveLength(3)
  const tasks = await (await originalFetch(`${endpoint}/api/v1/tasks?tenantId=test`, { headers })).json()
  expect(tasks.items).toEqual([])
  const crossTenant = await originalFetch(`${endpoint}/api/v1/changes/change-1`, { headers: { Authorization: 'Bearer other-api-test-token-at-least-32-characters' } })
  expect(crossTenant.status).toBe(404)
  const detail = screen.getByRole('region', { name: '需求详情' })
  expect(within(detail).getAllByText('d'.repeat(40))).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: '单仓库审阅' }))
  await screen.findByText('暂无 Task')
})
