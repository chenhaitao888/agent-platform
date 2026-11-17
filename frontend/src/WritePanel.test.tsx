import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import WritePanel from './WritePanel'
import { APIContext } from './api'
import type { DeliveryBundle } from './delivery'

const bundle: DeliveryBundle = { schemaVersion: 'delivery-archive.v1', id: 'delivery-1', tenantId: 'tenant-local', changeId: 'change-1', title: '测试写入', description: '', environment: 'test', createdBy: 'alice', createdAt: 'now', digest: 'a'.repeat(64), snapshot: { id: 'snapshot-1', revision: 1, state: 'READY', inputHash: 'snapshot', createdAt: 'now', services: [] }, plan: { id: 'plan-1', revision: 1, tenantId: 'tenant-local', environment: 'test', schemaVersion: 'change-plan.v1', snapshotId: 'snapshot-1', snapshotHash: 'snapshot', hash: 'hash', state: 'READY_FOR_REVIEW', createdAt: 'now', services: [], actions: [{ id: 'action-1', hash: 'hash', kind: 'config', key: 'timeout', operation: 'config.create', effect: 'publish_required', resourceVersion: 'v1', mustNotExist: true, target: { tenantId: 'tenant-local', environment: 'test', connectorRef: 'config', resourceRef: 'settings' }, proposals: [{ serviceId: 'S1', value: { type: 'integer', text: '200' }, evidence: [] }] }], issues: [] }, evidence: [] }
const permission = { tenantId: bundle.tenantId, bundleId: bundle.id, canApprove: true, canExecute: true, canRevoke: true }
const approval = { id: 'approval-1', tenantId: bundle.tenantId, bundleId: bundle.id, mode: 'WRITE', subject: 'bob', reason: '已复核', state: 'APPROVED', version: 1, expiresAt: '2099-01-01T00:00:00Z' }
const run = { id: 'write-1', tenantId: bundle.tenantId, bundleId: bundle.id, mode: 'WRITE', approvalId: approval.id, state: 'QUEUED', operations: [{ id: 'op-1', actionId: 'action-1', state: 'NOT_STARTED' }] }
const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const read = (path: string, approvals: unknown[] = [], runs: unknown[] = [], access = permission) => reply(path.endsWith('write-access') ? access : { items: path.endsWith('write-approvals') ? approvals : runs })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
async function fillApproval() {
  await waitFor(() => expect(screen.getByRole('button', { name: '批准写入' })).toBeEnabled())
  fireEvent.change(screen.getByLabelText('写入批准理由'), { target: { value: '已复核业务值与 Findings' } })
  fireEvent.click(screen.getByLabelText('已复核 Findings、配置值和实验定义，批准本交付包的写入'))
}

it('only writes after separate approval and execution clicks, sending references instead of a payload', async () => {
  const posts: { path: string; body: string }[] = []; let approved = false; let started = false
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return read(path, approved ? [approval] : [], started ? [run] : [])
    posts.push({ path, body: init.body as string })
    if (path.endsWith('write-approvals')) { approved = true; return reply(approval, 201) }
    started = true; return reply(run, 202)
  }))
  render(<WritePanel bundle={bundle} />)
  await fillApproval(); expect(posts).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '批准写入' }))
  await screen.findByText(/bob · 可用于写入/)
  expect(posts).toHaveLength(1)
  expect(screen.getByRole('button', { name: '执行配置与实验写入' })).toBeDisabled()
  fireEvent.click(screen.getByLabelText('确认执行 test 环境的配置与实验草稿变更'))
  fireEvent.click(screen.getByRole('button', { name: '执行配置与实验写入' }))
  await screen.findByText('请求已保存，等待执行和条件核验。')
  expect(JSON.parse(posts[0].body)).toEqual({ idempotencyKey: expect.any(String), expectedDigest: bundle.digest, ttlSeconds: 900, reason: '已复核业务值与 Findings', findingsReviewed: true })
  expect(JSON.parse(posts[1].body)).toEqual({ idempotencyKey: expect.any(String), expectedDigest: bundle.digest, approvalId: approval.id })
  expect(posts.map(p => p.body).join('')).not.toMatch(/intent|subject|connectorRef|secret|scenario|payload/)
})

it('reuses the exact request after a lost approval response and blocks another mutation', async () => {
  const bodies: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return read(path)
    bodies.push(init.body as string)
    if (bodies.length === 1) throw new TypeError('private provider diagnostic')
    return reply(approval)
  }))
  render(<WritePanel bundle={bundle} />); await fillApproval()
  fireEvent.click(screen.getByRole('button', { name: '批准写入' }))
  await screen.findByRole('button', { name: '重试本次写入操作' })
  expect(screen.getByRole('button', { name: '批准写入' })).toBeDisabled()
  expect(screen.getByRole('alert')).not.toHaveTextContent('private')
  fireEvent.click(screen.getByRole('button', { name: '重试本次写入操作' }))
  await waitFor(() => expect(bodies).toHaveLength(2)); expect(bodies[0]).toBe(bodies[1])
})

it.each(['disabled', 'read-only', 'blocked', 'expired', 'foreign'])('keeps mutations closed for %s state', async mode => {
  const mock = vi.fn(async (path: string) => mode === 'disabled' ? reply({ error: 'writes_unavailable' }, 503) : read(path, [{ ...approval, ...(mode === 'expired' ? { expiresAt: '2000-01-01T00:00:00Z' } : mode === 'foreign' ? { tenantId: 'foreign', reason: 'foreign-private-reason' } : {}) }], [], { ...permission, canApprove: mode !== 'read-only', canExecute: mode !== 'read-only' }))
  vi.stubGlobal('fetch', mock)
  render(<WritePanel bundle={mode === 'blocked' ? { ...bundle, plan: { ...bundle.plan, state: 'BLOCKED' } } : bundle} />)
  await waitFor(() => expect(screen.queryByText('正在读取写入状态…')).not.toBeInTheDocument())
  if (mode === 'disabled') expect(screen.queryByRole('button', { name: '执行配置与实验写入' })).not.toBeInTheDocument()
  else {
    fireEvent.click(screen.getByLabelText('确认执行 test 环境的配置与实验草稿变更'))
    expect(screen.getByRole('button', { name: '执行配置与实验写入' })).toBeDisabled()
  }
  expect(screen.queryByText('foreign-private-reason')).not.toBeInTheDocument()
  expect(mock.mock.calls).toHaveLength(3)
})

it('can only reconcile unknown outcomes and preserves APPLIED evidence after drift', async () => {
  let reconciled = false; const bodies: string[] = []
  const evidence = { ...run, state: 'UNKNOWN', operations: [{ ...run.operations[0], state: 'APPLIED', receipt: { id: 'receipt-1', status: 'APPLIED', effect: 'publish_required', recordedAt: 'saved' } }] }
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    const current = reconciled ? { ...evidence, state: 'DRIFTED', operations: [{ ...evidence.operations[0], state: 'DRIFTED', verification: { state: 'DRIFTED', observedAt: 'checked' } }] } : evidence
    if (init?.method !== 'POST') return read(path, [], [current], { ...permission, canApprove: false, canExecute: false })
    expect(path).toMatch(/\/write-executions\/write-1\/reconcile$/)
    bodies.push(init.body as string); reconciled = true; return reply({ ...current, state: 'DRIFTED' })
  }))
  render(<WritePanel bundle={bundle} />)
  fireEvent.click(await screen.findByRole('button', { name: '只读核对结果：write-1' }))
  await screen.findByRole('heading', { name: '写入后发现状态变化' })
  expect(bodies).toEqual(['{}'])
  expect(screen.getByText('receipt-1 · APPLIED · saved')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /只读核对结果/ })).not.toBeInTheDocument()
})

it('clears permissions and old results when a refresh is forbidden', async () => {
  let denied = false
  vi.stubGlobal('fetch', vi.fn(async (path: string) => denied ? reply({ error: 'change_forbidden' }, 403) : read(path, [approval])))
  render(<WritePanel bundle={bundle} />); await screen.findByText(/bob · 可用于写入/)
  denied = true; fireEvent.click(screen.getByRole('button', { name: '刷新写入记录' }))
  await screen.findByRole('alert')
  expect(screen.queryByText(/bob · 可用于写入/)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '批准写入' })).toBeDisabled()
})

it('aborts and discards a late mutation when the authenticated subject changes', async () => {
  let complete!: (response: Response) => void; let signal: AbortSignal | undefined | null
  const old = { tenantId: bundle.tenantId, fetch: vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return read(path)
    signal = init.signal; return new Promise<Response>(resolve => { complete = resolve })
  }) }
  const next = { tenantId: bundle.tenantId, fetch: vi.fn(async (path: string) => read(path, [], [], { ...permission, canApprove: false, canExecute: false })) }
  const view = render(<APIContext.Provider value={old}><WritePanel bundle={bundle} /></APIContext.Provider>)
  await fillApproval(); fireEvent.click(screen.getByRole('button', { name: '批准写入' }))
  await waitFor(() => expect(complete).toBeDefined())
  view.rerender(<APIContext.Provider value={next}><WritePanel bundle={bundle} /></APIContext.Provider>)
  expect(signal?.aborted).toBe(true)
  await act(async () => complete(reply(approval)))
  expect(screen.queryByText(/bob · 可用于写入/)).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '重试本次写入操作' })).not.toBeInTheDocument()
})
