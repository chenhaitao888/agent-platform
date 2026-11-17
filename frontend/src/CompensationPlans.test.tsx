import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import CompensationPlans from './CompensationPlans'
import WritePanel from './WritePanel'
import type { DeliveryBundle } from './delivery'

const bundle: DeliveryBundle = { schemaVersion: 'delivery-archive.v1', id: 'source', tenantId: 'tenant-local', changeId: 'change-1', title: 'original', description: '', environment: 'test', createdBy: 'alice', createdAt: 'now', digest: 'digest', snapshot: { id: 'snapshot-1', revision: 1, state: 'READY', inputHash: 'snapshot', createdAt: 'now', services: [] }, plan: { id: 'plan-1', revision: 1, tenantId: 'tenant-local', environment: 'test', schemaVersion: 'change-plan.v1', snapshotId: 'snapshot-1', snapshotHash: 'snapshot', hash: 'hash', state: 'READY_FOR_REVIEW', createdAt: 'now', services: [], actions: [{ id: 'a1', hash: 'hash', kind: 'config', key: 'timeout', operation: 'config.update', effect: 'publish_required', target: { tenantId: 'tenant-local', environment: 'test', connectorRef: 'config', resourceRef: 'namespace' }, resourceVersion: 'v3', expectedVersion: 'item-2', mustNotExist: false, currentValue: { type: 'integer', text: '200' }, proposals: [{ serviceId: 'S1', value: { type: 'integer', text: '100' }, evidence: [] }] }], issues: [] }, evidence: [] }
const runs = [{ id: 'run-1', state: 'FAILED', evidenceHash: 'f'.repeat(64) }]
const report = { schemaVersion: 'compensation-plan.v1', id: 'comp-1', tenantId: bundle.tenantId, sourceBundleId: bundle.id, sourceExecutionId: 'run-1', state: 'READY_FOR_REVIEW', reason: '人工确认原值', derivedBundleId: 'reverse', items: [{ operationId: 'op1', key: 'timeout', disposition: 'RESTORE_CONFIG', code: 'restore_previous_value', currentValue: { type: 'integer', text: '200' }, restoreValue: { type: 'integer', text: '100' } }, { operationId: 'op2', key: 'draft', disposition: 'MANUAL', code: 'deletion_unsupported' }] }
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
afterEach(() => vi.unstubAllGlobals())

it('prepares only on request and retains manual items beside a separately opened compensation bundle', async () => {
  let saved = false; const posts: { path: string; body: string }[] = []; const open = vi.fn()
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return reply({ items: saved ? [report] : [] })
    posts.push({ path, body: init.body as string }); saved = true; return reply(report, 201)
  })
  render(<CompensationPlans bundle={bundle} runs={runs} canPrepare onOpen={open} />)
  expect(posts).toHaveLength(0)
  fireEvent.change(screen.getByLabelText('处置理由'), { target: { value: '人工确认原值' } })
  fireEvent.click(screen.getByRole('button', { name: '准备处置与补偿计划' }))
  await screen.findByText('当前值：200 → 恢复值：100')
  expect(screen.getByText('draft · 人工处置')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '打开补偿交付包：reverse' }))
  expect(open).toHaveBeenCalledWith('reverse')
  expect(posts).toHaveLength(1)
  expect(JSON.parse(posts[0].body)).toEqual({ idempotencyKey: expect.any(String), expectedExecutionHash: runs[0].evidenceHash, reason: '人工确认原值' })
  expect(posts[0].path).toBe('/api/v1/deliveries/source/write-executions/run-1/compensations')
  expect(posts[0].body).not.toMatch(/restoreValue|intent|value|writer|subject/)
})

it('keeps the exact preparation request after a lost reply and hides private diagnostics', async () => {
  const bodies: string[] = []
  vi.stubGlobal('fetch', async (_: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return reply({ items: [] })
    bodies.push(init.body as string)
    if (bodies.length === 1) throw new Error('private platform token')
    return reply(report)
  })
  render(<CompensationPlans bundle={bundle} runs={runs} canPrepare />)
  fireEvent.change(screen.getByLabelText('处置理由'), { target: { value: '重试保持同一计划' } })
  fireEvent.click(screen.getByRole('button', { name: '准备处置与补偿计划' }))
  await screen.findByRole('button', { name: '重试本次补偿准备' })
  expect(screen.getByRole('alert')).not.toHaveTextContent('private')
  expect(screen.getByRole('button', { name: '准备处置与补偿计划' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: '重试本次补偿准备' }))
  await waitFor(() => expect(bodies).toHaveLength(2)); expect(bodies[0]).toBe(bodies[1])
})

it.each(['running', 'unprivileged', 'foreign'])('does not prepare or disclose a foreign plan for %s', async mode => {
  const mock = vi.fn(async () => reply({ items: mode === 'foreign' ? [{ ...report, tenantId: 'other', reason: 'private-other-tenant' }] : [] }))
  vi.stubGlobal('fetch', mock)
  render(<CompensationPlans bundle={bundle} runs={mode === 'running' ? [{ ...runs[0], state: 'RUNNING' }] : runs} canPrepare={mode !== 'unprivileged' && mode !== 'foreign'} />)
  await act(async () => {})
  expect(screen.getByRole('button', { name: '准备处置与补偿计划' })).toBeDisabled()
  expect(screen.queryByText('private-other-tenant')).not.toBeInTheDocument()
  expect(mock).toHaveBeenCalledTimes(1)
})

it('does not open an executable bundle for unknown outcomes', async () => {
  vi.stubGlobal('fetch', async () => reply({ items: [{ ...report, state: 'BLOCKED', derivedBundleId: undefined, errorCode: 'source_not_settled' }] }))
  render(<CompensationPlans bundle={bundle} runs={runs} canPrepare onOpen={() => {}} />)
  await screen.findByText('暂不能生成可执行补偿')
  expect(screen.queryByRole('button', { name: /打开补偿交付包/ })).not.toBeInTheDocument()
})

it('labels compensation approval and execution explicitly and never reuses a forward approval', async () => {
  const b = { ...bundle, id: 'reverse', compensation: { reportId: 'comp-1', bundleId: 'source', bundleDigest: 'digest', executionId: 'run-1', executionHash: 'hash', manualItems: 1 } }
  vi.stubGlobal('fetch', async (path: string) => reply(path.endsWith('write-access') ? { tenantId: b.tenantId, bundleId: b.id, canApprove: true, canExecute: true, canRevoke: true } : { items: [] }))
  render(<WritePanel bundle={b} />)
  await waitFor(() => expect(screen.getByRole('button', { name: '批准补偿' })).toBeEnabled())
  expect(screen.getByLabelText('已复核原回执、补偿值和人工事项，批准本补偿计划')).toBeInTheDocument()
  fireEvent.click(screen.getByLabelText('确认执行 test 环境的配置补偿'))
  expect(screen.getByRole('button', { name: '执行配置补偿' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: '准备处置与补偿计划' })).not.toBeInTheDocument()
})
