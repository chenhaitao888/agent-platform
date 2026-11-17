import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import SimulationPanel from './SimulationPanel'
import type { DeliveryBundle } from './delivery'
import { APIContext } from './api'

const bundle: DeliveryBundle = { schemaVersion: 'delivery-archive.v1', id: 'delivery-1', tenantId: 'tenant-local', changeId: 'change-1', title: '测试交付包', description: '', environment: 'test', createdBy: 'alice', createdAt: 'now', digest: 'a'.repeat(64), snapshot: { id: 'snapshot-1', revision: 1, state: 'READY', inputHash: 'snapshot', createdAt: 'now', services: [] }, plan: { id: 'plan-1', revision: 1, tenantId: 'tenant-local', environment: 'test', schemaVersion: 'change-plan.v1', snapshotId: 'snapshot-1', snapshotHash: 'snapshot', hash: 'hash', state: 'READY_FOR_REVIEW', createdAt: 'now', services: [], actions: [], issues: [] }, evidence: [] }
const approval = { id: 'approval-1', tenantId: 'tenant-local', bundleId: 'delivery-1', mode: 'SIMULATION', subject: 'alice', reason: '已核对', state: 'APPROVED', version: 1, expiresAt: '2030-01-01T00:00:00Z' }
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
afterEach(() => vi.unstubAllGlobals())

it('requires explicit approval and start actions and only sends bound simulation input', async () => {
  let approved = false
  let run = false
  const posts: { path: string; body: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      posts.push({ path, body: init.body as string })
      if (path.endsWith('simulation-approvals')) { approved = true; return reply(approval, 201) }
      run = true; return reply({ id: 'simulation-1', tenantId: bundle.tenantId, bundleId: bundle.id }, 202)
    }
    return reply({ items: path.endsWith('simulation-approvals') ? approved ? [approval] : [] : run ? [{ id: 'simulation-1', tenantId: bundle.tenantId, bundleId: bundle.id, approvalId: approval.id, scenario: 'success', state: 'QUEUED', operations: [] }] : [] })
  }))
  const { unmount } = render(<SimulationPanel bundle={bundle} />)
  await waitFor(() => expect(screen.getByRole('button', { name: '批准模拟' })).toBeEnabled())
  expect(posts).toHaveLength(0)
  fireEvent.change(screen.getByLabelText('模拟批准理由'), { target: { value: '已核对需求和 Findings' } })
  fireEvent.click(screen.getByLabelText('已核对 Findings 和变更计划，仅批准模拟'))
  fireEvent.click(screen.getByRole('button', { name: '批准模拟' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '发起模拟' })).toBeEnabled())
  expect(posts).toHaveLength(1)
  expect(JSON.parse(posts[0].body)).toEqual({ idempotencyKey: expect.any(String), expectedDigest: bundle.digest, ttlSeconds: 3600, reason: '已核对需求和 Findings', findingsReviewed: true })
  fireEvent.click(screen.getByRole('button', { name: '发起模拟' }))
  await screen.findByText('请求已持久保存，等待 Worker 调度和核验。')
  expect(posts).toHaveLength(2)
  expect(JSON.parse(posts[1].body)).toEqual({ idempotencyKey: expect.any(String), expectedDigest: bundle.digest, approvalId: approval.id, scenario: 'success' })
  expect(posts.map(p => p.body).join('')).not.toMatch(/payload|credential|connectorRef|subject/)
  unmount()
})

it('retains the exact approval request after an uncertain server result', async () => {
  const bodies: string[] = []
  let saved = false
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return reply({ items: saved && path.endsWith('simulation-approvals') ? [approval] : [] })
    bodies.push(init.body as string)
    if (bodies.length === 1) return reply({ error: 'archive_unavailable' }, 503)
    saved = true; return reply(approval)
  }))
  render(<SimulationPanel bundle={bundle} />)
  await waitFor(() => expect(screen.getByRole('button', { name: '批准模拟' })).toBeEnabled())
  fireEvent.change(screen.getByLabelText('模拟批准理由'), { target: { value: '同一批准' } })
  fireEvent.click(screen.getByLabelText('已核对 Findings 和变更计划，仅批准模拟'))
  fireEvent.click(screen.getByRole('button', { name: '批准模拟' }))
  fireEvent.click(await screen.findByRole('button', { name: '重试本次模拟操作' }))
  await screen.findByText(/alice · 可用于模拟/)
  expect(bodies[0]).toBe(bodies[1])
})

it('does not let a blocked delivery approve or execute a simulation', async () => {
  const mock = vi.fn(async () => reply({ items: [] }))
  vi.stubGlobal('fetch', mock)
  render(<SimulationPanel bundle={{ ...bundle, plan: { ...bundle.plan, state: 'BLOCKED' } }} />)
  await waitFor(() => expect(screen.queryByText('正在读取模拟状态…')).not.toBeInTheDocument())
  expect(screen.getByRole('button', { name: '批准模拟' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '发起模拟' })).toBeDisabled()
  expect(mock.mock.calls).toHaveLength(2)
})

it('drops a late mutation response when leaving the delivery identity', async () => {
  let finish!: (response: Response) => void
  let signal: AbortSignal | null | undefined
  const api = { tenantId: 'tenant-local', fetch: vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === 'POST') { signal = init.signal; return new Promise<Response>(resolve => { finish = resolve }) }
    return reply({ items: [] })
  }) }
  const { unmount } = render(<APIContext.Provider value={api}><SimulationPanel bundle={bundle} /></APIContext.Provider>)
  await waitFor(() => expect(screen.getByRole('button', { name: '批准模拟' })).toBeEnabled())
  fireEvent.change(screen.getByLabelText('模拟批准理由'), { target: { value: '绑定当前身份' } })
  fireEvent.click(screen.getByLabelText('已核对 Findings 和变更计划，仅批准模拟'))
  fireEvent.click(screen.getByRole('button', { name: '批准模拟' }))
  await waitFor(() => expect(finish).toBeDefined())
  unmount(); expect(signal?.aborted).toBe(true)
  await act(async () => finish(reply(approval)))
  expect(screen.queryByText(/alice · 可用于模拟/)).not.toBeInTheDocument()
})
