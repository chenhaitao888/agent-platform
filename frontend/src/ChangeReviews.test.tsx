import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useCallback, useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'
import ChangeReviews from './ChangeReviews'
import { APIContext } from './api'
import type { ChangeRequest, ChangeReviewRun } from './change'

const snapshot = { id: 'snapshot-1', revision: 1, state: 'READY' as const, inputHash: 'hash-1', createdAt: 'now', services: ['S1','S2','S3'].map(serviceId => ({ serviceId, sourceBranch: 'feature/A', targetBranch: 'main', catalogVersion: '1', resolvedAt: 'now', repository: { provider: 'gitlab' as const, repositoryId: serviceId, targetBranch: 'main', headSha: 'a'.repeat(40), targetSha: 'b'.repeat(40), baseSha: 'c'.repeat(40) } })) }
const change: ChangeRequest = { id: 'change-1', tenantId: 'tenant-local', title: '需求 A', description: '', environment: 'test', version: 2, services: snapshot.services, currentSnapshotId: snapshot.id, snapshots: [snapshot], createdAt: 'now' }
const result = (state: 'PARTIAL' | 'SUCCEEDED', id = 'change-review-1'): ChangeReviewRun => ({ id, snapshotId: 'snapshot-1', snapshotHash: 'hash-1', state, startedAt: 'now', services: ['S1','S2','S3'].map((serviceId,i) => ({ serviceId, state: state === 'PARTIAL' && i === 1 ? 'FAILED' : 'SUCCEEDED', stage: 'review', taskId: `task-${i+1}`, workspaceId: `workspace-${i+1}`, workspaceVersion: 3, executionId: `review-${i+1}`, ...(state === 'PARTIAL' && i === 1 ? { errorCode: 'review_findings_invalid' } : { artifactId: `artifact-${i+1}` }) })) })
const reply = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function Harness({ value = change }: { value?: ChangeRequest }) {
  const [current,setCurrent] = useState(value)
  const [busy,setBusy] = useState(false)
  const update = useCallback((version: number) => setCurrent(v => ({ ...v, version: Math.max(v.version,version) })), [])
  return <ChangeReviews change={current} onVersion={update} onBusy={setBusy} blocked={busy} />
}
afterEach(() => vi.unstubAllGlobals())

it('starts only explicitly, replays a lost response unchanged and keeps earlier failures after retry', async () => {
  let runs: ChangeReviewRun[] = []
  const posts: string[] = []
  const mock = vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      posts.push(init.body as string)
      if (posts.length === 1) { runs = [result('PARTIAL')]; throw new TypeError('secret upstream message') }
      if (posts.length === 3) runs = [...runs, result('SUCCEEDED','change-review-2')]
      return reply({ changeId: change.id, changeVersion: posts.length === 3 ? 6 : 4, review: runs.at(-1) },201)
    }
    return reply(path.endsWith('/reviews') ? { items: runs } : { ...change, version: runs.length ? 4 : 2 })
  })
  vi.stubGlobal('fetch',mock)
  render(<Harness />)
  await screen.findByText('该快照尚未审阅。查看快照或刷新状态不会调用模型。')
  fireEvent.click(screen.getByRole('button',{ name: '刷新审阅状态' }))
  await waitFor(() => expect(mock).toHaveBeenCalledTimes(4))
  expect(posts).toHaveLength(0)
  fireEvent.click(screen.getByRole('button',{ name: '开始多服务审阅' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('审阅结果未确认')
  expect(screen.getByRole('alert')).not.toHaveTextContent('secret')
  fireEvent.click(screen.getByRole('button',{ name: '重试本次审阅请求' }))
  await screen.findByRole('button',{ name: '重试未完成服务' })
  expect(posts[1]).toBe(posts[0])
  fireEvent.click(screen.getByRole('button',{ name: '重试未完成服务' }))
  await screen.findByRole('heading',{ name: '快照 1 · 已完成审阅' })
  expect(screen.getByRole('heading',{ name: '快照 1 · 部分完成' })).toBeInTheDocument()
  expect(JSON.parse(posts[2]).expectedVersion).toBe(4)
  expect(JSON.parse(posts[2]).idempotencyKey).not.toBe(JSON.parse(posts[0]).idempotencyKey)
  expect(screen.getByRole('button',{ name: '开始多服务审阅' })).toBeDisabled()
})

it('binds old results to their snapshot and never treats them as a review of a new snapshot', async () => {
  const next = { ...change, version: 5, currentSnapshotId: 'snapshot-2', snapshots: [snapshot,{ ...snapshot,id: 'snapshot-2',revision: 2,inputHash:'hash-2' }] }
  const mock = vi.fn(async (path: string) => reply(path.endsWith('/reviews') ? { items: [result('SUCCEEDED')] } : next))
  vi.stubGlobal('fetch',mock)
  render(<Harness value={next} />)
  await screen.findByText('该快照尚未审阅。查看快照或刷新状态不会调用模型。')
  expect(screen.queryByText('快照 1 · 已完成审阅')).not.toBeInTheDocument()
  expect(screen.getByRole('button',{ name:'开始多服务审阅' })).toBeEnabled()
  fireEvent.change(screen.getByLabelText('审阅快照'),{ target: { value:'snapshot-1' } })
  expect(screen.getByRole('note')).toHaveTextContent('历史快照 1')
  expect(screen.getByRole('heading',{ name:'快照 1 · 已完成审阅' })).toBeInTheDocument()
  expect(screen.getByRole('button',{ name:'开始多服务审阅' })).toBeDisabled()
  expect(mock.mock.calls).toHaveLength(2)
})

it('rejects mismatched child evidence before reading execution or artifact content', async () => {
  const mock = vi.fn(async (path: string) => {
    if (path.includes('/tasks/')) return reply({ id:'task-1',tenantId:'tenant-local',repository:snapshot.services[0].repository,change:{changeId:'change-1',snapshotId:'other-snapshot',serviceId:'S1'} })
    return reply(path.endsWith('/reviews') ? { items:[result('SUCCEEDED')] } : change)
  })
  vi.stubGlobal('fetch',mock)
  render(<Harness />)
  fireEvent.click(await screen.findByRole('button',{ name:'查看 S1 结果' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('无法读取与该服务快照一致的任务记录')
  expect(mock.mock.calls.some(([path]) => path.includes('/artifacts/') || path.includes('/tasks/task-1/reviews/'))).toBe(false)
})

it('drops late execution responses when the authenticated view is unmounted', async () => {
  let finish!: (response: Response) => void
  let signal: AbortSignal | null | undefined
  const oldAPI = { tenantId: 'tenant-local', fetch: vi.fn(async (path: string,init?: RequestInit) => {
    if (init?.method === 'POST') { signal=init.signal; return new Promise<Response>(resolve => { finish=resolve }) }
    return reply(path.endsWith('/reviews') ? {items:[]} : change)
  }) }
  const nextAPI = { tenantId:'new-tenant', fetch: vi.fn(async (path: string) => reply(path.endsWith('/reviews') ? {items:[]} : {...change,tenantId:'new-tenant'})) }
  const { rerender }=render(<APIContext.Provider value={oldAPI}><Harness key="old" /></APIContext.Provider>)
  await screen.findByText('该快照尚未审阅。查看快照或刷新状态不会调用模型。')
  fireEvent.click(screen.getByRole('button',{name:'开始多服务审阅'}))
  await waitFor(() => expect(finish).toBeDefined())
  rerender(<APIContext.Provider value={nextAPI}><Harness key="new" value={{...change,tenantId:'new-tenant'}} /></APIContext.Provider>)
  expect(signal?.aborted).toBe(true)
  await act(async () => finish(reply({changeId:change.id,changeVersion:4,review:result('SUCCEEDED')})))
  await screen.findByText('该快照尚未审阅。查看快照或刷新状态不会调用模型。')
  expect(within(screen.getByRole('region',{name:'需求审阅'})).queryByText('快照 1 · 已完成审阅')).not.toBeInTheDocument()
})
