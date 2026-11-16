import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useCallback, useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'
import ChangePlans from './ChangePlans'
import { APIContext } from './api'
import type { ChangeRequest } from './change'
import type { ChangePlan } from './planning'

const change: ChangeRequest = { id: 'change-1', tenantId: 'tenant-local', title: 'A', description: '', environment: 'test', version: 4, services: [{ serviceId: 'S1', sourceBranch: 'feature/A', targetBranch: 'main' }], currentSnapshotId: 'snapshot-1', createdAt: 'now', snapshots: [{ id: 'snapshot-1', revision: 1, state: 'READY', inputHash: 'hash-1', createdAt: 'now', services: [{ serviceId: 'S1', sourceBranch: 'feature/A', targetBranch: 'main', catalogVersion: '1', resolvedAt: 'now' }] }] }
const plan = (id = 'plan-1'): ChangePlan => ({ id, revision: id === 'plan-1' ? 1 : 2, tenantId: 'tenant-local', environment: 'test', snapshotId: 'snapshot-1', snapshotHash: 'hash-1', schemaVersion: 'change-plan.v1', state: 'BLOCKED', hash: id, createdAt: 'now', services: [{ serviceId: 'S1', headSha: 'head', baseSha: 'base', configCoverage: 'NO_CANDIDATES', experimentCoverage: 'NO_CANDIDATES', review: { state: 'SUCCEEDED' }, candidates: [] }], actions: [], issues: [{ code: 'coverage_confirmation_required', serviceId: 'S1', kind: 'config' }] })
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function Harness({ value = change }: { value?: ChangeRequest }) {
  const [current, setCurrent] = useState(value)
  const [busy, setBusy] = useState(false)
  const update = useCallback((version: number) => setCurrent(v => ({ ...v, version: Math.max(v.version, version) })), [])
  return <ChangePlans change={current} onVersion={update} onBusy={setBusy} blocked={busy} />
}
afterEach(() => vi.unstubAllGlobals())

it('only generates on explicit action and replays a lost response with identical business input', async () => {
  const posts: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return reply({ items: [] })
    posts.push(init.body as string)
    if (posts.length === 1) throw new TypeError('secret upstream')
    return reply({ changeId: 'change-1', changeVersion: 6, plan: plan() }, 201)
  }))
  render(<Harness />)
  await screen.findByText('此快照尚未生成配置与实验计划。')
  expect(posts).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('计划结果未确认')
  expect(screen.getByRole('alert')).not.toHaveTextContent('secret')
  fireEvent.click(screen.getByRole('button', { name: '重试本次计划请求' }))
  await screen.findByRole('heading', { name: '计划存在待补齐或阻断项' })
  expect(posts[0]).toBe(posts[1])
  expect(screen.getByText(/未发现候选，待确认覆盖/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /执行|批准|激活/ })).not.toBeInTheDocument()
})

it('submits typed business values and explicit coverage reasons without target overrides', async () => {
  const posts: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return reply({ items: [] })
    posts.push(init.body as string)
    return reply({ changeId: change.id, changeVersion: 6, plan: { ...plan(), state: 'READY_FOR_REVIEW', issues: [] } })
  }))
  render(<Harness />)
  await screen.findByText('此快照尚未生成配置与实验计划。')
  fireEvent.click(screen.getByText('S1 · 补充业务要求与覆盖依据'))
  fireEvent.click(screen.getByRole('button', { name: '添加配置要求：S1' }))
  fireEvent.change(screen.getByLabelText('S1 配置 1 名称'), { target: { value: 'feature.timeout' } })
  fireEvent.change(screen.getByLabelText('S1 配置 1 类型'), { target: { value: 'integer' } })
  fireEvent.change(screen.getByLabelText('S1 配置 1 期望值'), { target: { value: '200' } })
  fireEvent.change(screen.getByLabelText('S1 配置 1 理由'), { target: { value: '验收要求' } })
  fireEvent.click(screen.getByLabelText('S1 确认不涉及实验'))
  fireEvent.change(screen.getByLabelText('S1 实验不涉及依据'), { target: { value: '已核对实验调用无变化' } })
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  await screen.findByText('计划已整理 · 待人工审阅')
  const input = JSON.parse(posts[0])
  expect(input.requirements[0]).toEqual({ serviceId: 'S1', config: [{ key: 'feature.timeout', desired: { type: 'integer', text: '200' }, reason: '验收要求' }], experimentNotInvolved: '已核对实验调用无变化' })
  expect(posts[0]).not.toMatch(/resourceRef|connectorRef|tenantId|headSha|baseUrl/)
})

it('keeps plan history bound to its snapshot and clears draft values when switching snapshots', async () => {
  const next = { ...change, currentSnapshotId: 'snapshot-2', snapshots: [...change.snapshots, { ...change.snapshots[0], id: 'snapshot-2', revision: 2, inputHash: 'hash-2' }] }
  const mock = vi.fn(async () => reply({ items: [plan()] }))
  vi.stubGlobal('fetch', mock)
  render(<Harness value={next} />)
  await screen.findByText('此快照尚未生成配置与实验计划。')
  expect(screen.queryByText('计划存在待补齐或阻断项')).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('计划快照'), { target: { value: 'snapshot-1' } })
  await screen.findByText('计划存在待补齐或阻断项')
  expect(screen.getByRole('note')).toHaveTextContent('历史快照 1')
  fireEvent.click(screen.getByText('S1 · 补充业务要求与覆盖依据'))
  fireEvent.click(screen.getByRole('button', { name: '添加配置要求：S1' }))
  fireEvent.change(screen.getByLabelText('S1 配置 1 名称'), { target: { value: 'old.requirement' } })
  fireEvent.change(screen.getByLabelText('计划快照'), { target: { value: 'snapshot-2' } })
  await screen.findByText('此快照尚未生成配置与实验计划。')
  expect(screen.queryByDisplayValue('old.requirement')).not.toBeInTheDocument()
  expect(mock.mock.calls).toHaveLength(3)
})

it('ignores a late plan response after leaving the authenticated view', async () => {
  let finish!: (response: Response) => void
  let signal: AbortSignal | null | undefined
  const api = { tenantId: 'tenant-local', fetch: vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === 'POST') { signal = init.signal; return new Promise<Response>(resolve => { finish = resolve }) }
    return reply({ items: [] })
  }) }
  const { unmount } = render(<APIContext.Provider value={api}><Harness /></APIContext.Provider>)
  await screen.findByText('此快照尚未生成配置与实验计划。')
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  await waitFor(() => expect(finish).toBeDefined())
  unmount(); expect(signal?.aborted).toBe(true)
  await act(async () => finish(reply({ changeId: change.id, changeVersion: 6, plan: plan() })))
  expect(screen.queryByText('计划存在待补齐或阻断项')).not.toBeInTheDocument()
})
