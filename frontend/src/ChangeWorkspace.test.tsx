import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ChangeWorkspace from './ChangeWorkspace'
import { APIContext } from './api'

const services = ['S1', 'S2', 'S3'].map((id, i) => ({ serviceId: id, name: id, catalogVersion: '1', defaultTargetBranch: ['main', 'release/test', 'master'][i], allowedTargetBranches: [ ['main', 'release/test', 'master'][i], 'release/new'], environments: ['test'] }))
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
afterEach(() => vi.unstubAllGlobals())

it('creates a requirement from authorized services and explicitly freezes three branches', async () => {
  const calls: { path: string; init?: RequestInit }[] = []
  let record: Record<string, unknown> | null = null
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    calls.push({ path, init })
    if (path === '/api/v1/services') return reply({ items: services })
    if (path === '/api/v1/changes' && !init?.method) return reply({ items: record ? [record] : [] })
    if (path === '/api/v1/changes' && init?.method === 'POST') {
      const body = JSON.parse(init.body as string)
      record = { ...body, id: 'change-1', tenantId: 'tenant-local', version: 1, createdAt: '2026-11-13T00:00:00Z', snapshots: [] }
      return reply(record, 201)
    }
    if (path.endsWith('/snapshots')) {
      const body = JSON.parse(init!.body as string)
      const snapshot = { id: 'snapshot-1', revision: 1, state: 'READY', inputHash: 'hash', createdAt: '2026-11-13T01:00:00Z', services: body.services.map((s: {serviceId: string; sourceBranch: string; targetBranch: string}) => ({ ...s, catalogVersion: '1', resolvedAt: '2026-11-13T01:00:00Z', repository: { repositoryId: s.serviceId, headSha: 'a'.repeat(40), targetSha: 'b'.repeat(40), baseSha: 'c'.repeat(40), targetBranch: s.targetBranch } })) }
      record = { ...record, version: 2, currentSnapshotId: 'snapshot-1', snapshots: [snapshot] }
      return reply({ changeId: 'change-1', changeVersion: 2, snapshot }, 201)
    }
    if (path === '/api/v1/changes/change-1') return reply(record)
    if (path === '/api/v1/changes/change-1/reviews') return reply({ items: [] })
    if (path === '/api/v1/changes/change-1/plans') return reply({ items: [] })
    throw new Error(`unexpected ${path}`)
  }))
  render(<ChangeWorkspace />)
  await screen.findByText('暂无需求')
  fireEvent.change(screen.getByLabelText('需求名称或编号'), { target: { value: '需求 A' } })
  fireEvent.change(screen.getByLabelText('验收说明'), { target: { value: '三个服务共同完成验收' } })
  for (const service of services) {
    fireEvent.click(screen.getByLabelText(`选择 ${service.name}`))
    fireEvent.change(screen.getByLabelText(`${service.name} 源分支`), { target: { value: 'feature/A' } })
  }
  fireEvent.click(screen.getByRole('button', { name: '创建需求' }))
  const detail = await screen.findByRole('region', { name: '需求详情' })
  expect(within(detail).getByText('尚未固定快照')).toBeInTheDocument()
  expect(calls.filter(c => c.init?.method === 'POST')).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: '固定代码快照' }))
  await screen.findByText('快照已固定')
  const snapshotCall = calls.find(c => c.path.endsWith('/snapshots'))!
  const body = JSON.parse(snapshotCall.init!.body as string)
  expect(body.expectedVersion).toBe(1)
  expect(body.services.map((s: { targetBranch: string }) => s.targetBranch)).toEqual(['main', 'release/test', 'master'])
  expect(JSON.stringify(body)).not.toContain('Sha')
  expect(JSON.stringify(body)).not.toContain('repositoryId')
  expect(calls.some(c => c.init?.method === 'POST' && (c.path.includes('/tasks') || c.path.includes('/review')))).toBe(false)
  expect(within(detail).getAllByText('a'.repeat(40))).toHaveLength(3)
})

it('clears the previous tenant and ignores its late list response', async () => {
  let finish!: (response: Response) => void
  const oldFetch = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
  const newFetch = vi.fn(async () => reply({ items: [] }))
  const { rerender } = render(<APIContext.Provider value={{ tenantId: 'old', fetch: oldFetch }}><ChangeWorkspace /></APIContext.Provider>)
  await waitFor(() => expect(oldFetch).toHaveBeenCalled())
  rerender(<APIContext.Provider value={{ tenantId: 'new', fetch: newFetch }}><ChangeWorkspace /></APIContext.Provider>)
  await screen.findByText('暂无需求')
  finish(reply({ items: [{ id: 'old-record', title: '旧租户秘密' }] }))
  await waitFor(() => expect(oldFetch.mock.calls.length).toBeGreaterThan(0))
  expect(screen.queryByText('旧租户秘密')).not.toBeInTheDocument()
})

const existingChange = (id: string, title: string) => ({
  id, title, tenantId: 'tenant-local', description: '', environment: 'test', version: 1,
  services: services.map(s => ({ serviceId: s.serviceId, sourceBranch: 'feature/A', targetBranch: s.defaultTargetBranch })),
  snapshots: [], createdAt: '2026-11-13T00:00:00Z',
})

it('retries a lost snapshot response with the same key and version, then shows a partial failure honestly', async () => {
  const record = existingChange('change-1', '需要核验的需求')
  const posts: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/v1/services') return reply({ items: services })
    if (path === '/api/v1/changes') return reply({ items: [record] })
    if (path === '/api/v1/changes/change-1') return reply(record)
    if (path.endsWith('/snapshots')) {
      posts.push(init!.body as string)
      if (posts.length === 1) throw new TypeError('private credential echo')
      return reply({ changeId: record.id, changeVersion: 2, snapshot: { id: 'snapshot-1', revision: 1, state: 'FAILED', inputHash: 'hash', services: record.services.map((s, i) => ({ ...s, catalogVersion: '1', resolvedAt: 'now', ...(i === 1 ? { errorCode: 'source_branch_not_found' } : { repository: { headSha: 'a'.repeat(40), targetSha: 'b'.repeat(40), baseSha: 'c'.repeat(40) } }) })) } })
    }
    throw new Error('unexpected path')
  }))
  render(<ChangeWorkspace />)
  fireEvent.click(await screen.findByRole('button', { name: '需要核验的需求 · change-1' }))
  fireEvent.click(await screen.findByRole('button', { name: '固定代码快照' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('快照结果未确认')
  expect(screen.getByRole('alert')).not.toHaveTextContent('private')
  expect(screen.getByLabelText('快照 S1 源分支')).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: '重试本次快照' }))
  await screen.findByText('快照未完成 · 请检查失败服务后创建新快照')
  expect(posts).toHaveLength(2)
  expect(posts[0]).toBe(posts[1])
  expect(screen.getByText('源分支不存在或不可访问')).toBeInTheDocument()
  expect(screen.queryByText('快照已固定')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '创建新快照' })).toBeEnabled()
})

it('ignores late details after selecting another requirement and keeps history reads read-only', async () => {
  const first = existingChange('change-1', '第一个需求')
  const second = existingChange('change-2', '第二个需求')
  let finish!: (response: Response) => void
  const mock = vi.fn(async (path: string) => {
    if (path === '/api/v1/services') return reply({ items: services })
    if (path === '/api/v1/changes') return reply({ items: [first, second] })
    if (path.endsWith('change-1')) return new Promise<Response>(resolve => { finish = resolve })
    return reply(second)
  })
  vi.stubGlobal('fetch', mock)
  render(<ChangeWorkspace />)
  fireEvent.click(await screen.findByRole('button', { name: '第一个需求 · change-1' }))
  fireEvent.click(screen.getByRole('button', { name: '第二个需求 · change-2' }))
  const detail = screen.getByRole('region', { name: '需求详情' })
  await within(detail).findByRole('heading', { name: '第二个需求' })
  finish(reply(first))
  await waitFor(() => expect(mock).toHaveBeenCalledTimes(4))
  expect(within(detail).queryByRole('heading', { name: '第一个需求' })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '重新读取详情' }))
  await within(detail).findByRole('heading', { name: '第二个需求' })
  expect(mock.mock.calls.every(([path]) => !path.includes('/snapshots'))).toBe(true)
})

it('clears data when the authenticated subject changes within one tenant', async () => {
  const firstAPI = { tenantId: 'same', fetch: vi.fn(async (path: string) => reply({ items: path.endsWith('services') ? services : [existingChange('change-1', '前一个身份的需求')] })) }
  const nextAPI = { tenantId: 'same', fetch: vi.fn(async () => reply({ items: [] })) }
  const { rerender } = render(<APIContext.Provider value={firstAPI}><ChangeWorkspace /></APIContext.Provider>)
  await screen.findByRole('button', { name: '前一个身份的需求 · change-1' })
  rerender(<APIContext.Provider value={nextAPI}><ChangeWorkspace /></APIContext.Provider>)
  await screen.findByText('暂无需求')
  expect(screen.queryByRole('button', { name: '前一个身份的需求 · change-1' })).not.toBeInTheDocument()
})
