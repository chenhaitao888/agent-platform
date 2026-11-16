import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ArchivePlan from './ArchivePlan'
import DeliveryPackages from './DeliveryPackages'
import { APIContext } from './api'
import type { DeliveryBundle } from './delivery'

const bundle = (id: string, title: string): DeliveryBundle => ({ schemaVersion: 'delivery-archive.v1', id, title, tenantId: 'tenant-local', changeId: 'change-1', description: '', environment: 'test', createdBy: 'alice', createdAt: 'now', digest: 'd'.repeat(64), snapshot: { id: 'snapshot-1', revision: 1, inputHash: 'snapshot-hash', state: 'READY', createdAt: 'now', services: [] }, plan: { id: 'plan-1', revision: 1, schemaVersion: 'change-plan.v1', tenantId: 'tenant-local', environment: 'test', snapshotId: 'snapshot-1', snapshotHash: 'snapshot-hash', state: 'BLOCKED', hash: 'a'.repeat(64), createdAt: 'now', services: [], actions: [], issues: [] }, evidence: [] })
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
afterEach(() => vi.unstubAllGlobals())

it('saves only on an explicit click and replays an uncertain save with exactly the same body', async () => {
  const saved = bundle('delivery-one', '保存的需求')
  const bodies: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init?: RequestInit) => {
    bodies.push(init!.body as string)
    if (bodies.length === 1) throw new TypeError('private database detail')
    return reply(saved)
  }))
  render(<ArchivePlan changeId="change-1" plan={saved.plan} blocked={false} onBusy={() => {}} />)
  expect(bodies).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '保存当前计划为交付包' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('保存结果未确认')
  expect(screen.getByRole('alert')).not.toHaveTextContent('private')
  fireEvent.click(screen.getByRole('button', { name: '重试本次保存' }))
  await screen.findByText('已保存，可从“交付包”查看。保存不代表批准或执行。')
  expect(bodies[0]).toBe(bodies[1])
  expect(Object.keys(JSON.parse(bodies[0])).sort()).toEqual(['expectedPlanHash', 'idempotencyKey'])
  expect(screen.getByRole('button', { name: '交付包已保存' })).toBeDisabled()
})

it('rejects mismatched archive responses without displaying foreign content', async () => {
  const source = bundle('delivery-one', '当前需求')
  vi.stubGlobal('fetch', vi.fn(async () => reply({ ...source, tenantId: 'another', title: '秘密需求' })))
  render(<ArchivePlan changeId="change-1" plan={source.plan} blocked={false} onBusy={() => {}} />)
  fireEvent.click(screen.getByRole('button', { name: '保存当前计划为交付包' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('保存结果未确认')
  expect(screen.queryByText('秘密需求')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '交付包已保存' })).not.toBeInTheDocument()
})

it('keeps the idempotency key after a server error that may follow a committed save', async () => {
  const saved = bundle('delivery-committed', '已提交但回复失败')
  const bodies: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init?: RequestInit) => {
    bodies.push(init!.body as string)
    return bodies.length === 1 ? reply({ error: 'archive_unavailable' }, 503) : reply(saved)
  }))
  render(<ArchivePlan changeId="change-1" plan={saved.plan} blocked={false} onBusy={() => {}} />)
  fireEvent.click(screen.getByRole('button', { name: '保存当前计划为交付包' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('保留本次标识')
  fireEvent.click(screen.getByRole('button', { name: '重试本次保存' }))
  await screen.findByText('已保存，可从“交付包”查看。保存不代表批准或执行。')
  expect(bodies[0]).toBe(bodies[1])
})

it('keeps late package detail responses from replacing the selected saved plan', async () => {
  const first = bundle('delivery-one', '第一个需求')
  const second = bundle('delivery-two', '第二个需求')
  let finish!: (response: Response) => void
  const mock = vi.fn(async (path: string) => {
    if (path === '/api/v1/deliveries') return reply({ items: [first, second] })
    if (path.endsWith('delivery-one')) return new Promise<Response>(resolve => { finish = resolve })
    return reply(second)
  })
  vi.stubGlobal('fetch', mock)
  render(<DeliveryPackages />)
  fireEvent.click(await screen.findByRole('button', { name: '第一个需求 · delivery-one' }))
  fireEvent.click(screen.getByRole('button', { name: '第二个需求 · delivery-two' }))
  const detail = screen.getByRole('region', { name: '交付包详情' })
  await within(detail).findByRole('heading', { name: '第二个需求' })
  await act(async () => finish(reply(first)))
  expect(within(detail).queryByRole('heading', { name: '第一个需求' })).not.toBeInTheDocument()
  expect(mock.mock.calls.every(([path]) => path.startsWith('/api/v1/deliveries'))).toBe(true)
})

it('clears saved packages when the authenticated subject changes inside the same tenant', async () => {
  let finish!: (response: Response) => void
  const oldAPI = { tenantId: 'tenant-local', fetch: vi.fn(() => new Promise<Response>(resolve => { finish = resolve })) }
  const nextAPI = { tenantId: 'tenant-local', fetch: vi.fn(async () => reply({ items: [] })) }
  const { rerender } = render(<APIContext.Provider value={oldAPI}><DeliveryPackages /></APIContext.Provider>)
  await waitFor(() => expect(oldAPI.fetch).toHaveBeenCalled())
  rerender(<APIContext.Provider value={nextAPI}><DeliveryPackages /></APIContext.Provider>)
  await screen.findByText('暂无已保存的交付包')
  await act(async () => finish(reply({ items: [bundle('delivery-old', '旧身份内容')] })))
  expect(screen.queryByText(/旧身份内容/)).not.toBeInTheDocument()
})
