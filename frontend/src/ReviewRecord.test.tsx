import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import TaskEventTimeline from './TaskEventTimeline'
import ReviewRecord from './ReviewRecord'
import { APIContext, authenticatedFetch } from './api'
import { deferredResponse, findingsText, installReviewCrypto, reviewExecution, reviewTask } from './test/reviewFixtures'

beforeEach(installReviewCrypto)
afterEach(() => vi.unstubAllGlobals())

const token = 'record-view-test-token-at-least-32-characters'
const api = { tenantId: reviewTask.tenantId, fetch: authenticatedFetch(token, new AbortController().signal) }
const first = reviewExecution()
const reference = { executionId: first.id, workspaceId: first.workspaceId, workspaceVersion: first.workspaceVersion, artifactId: first.artifact!.id }
const events = [
  { eventId: 'event-1', sequence: 6, eventType: 'review.succeeded', occurredAt: first.startedAt, payload: { review: { ...reference, state: 'SUCCEEDED' } } },
  { eventId: 'event-2', sequence: 8, eventType: 'review.failed', occurredAt: first.startedAt, payload: { review: { ...reference, executionId: 'review-2', artifactId: undefined, state: 'FAILED', errorCode: 'review_findings_invalid' } } },
]

it('opens an earlier execution from its event and reads its original Findings without starting another review', async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ items: events }))
    .mockResolvedValueOnce(Response.json(first)).mockResolvedValueOnce(new Response(findingsText))
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><TaskEventTimeline task={reviewTask} /></APIContext.Provider>)
  expect(fetchMock).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))
  fireEvent.click(await screen.findByRole('button', { name: '查看执行 review-1（事件 6）' }))
  const record = await screen.findByRole('region', { name: '执行 review-1 的记录' })
  fireEvent.click(await within(record).findByRole('button', { name: '读取审阅结果' }))
  await within(record).findByText('Restore the zero guard')
  expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
    '/api/v1/tasks/task-1/events?tenantId=tenant-a',
    '/api/v1/tasks/task-1/reviews/review-1?tenantId=tenant-a',
    '/api/v1/artifacts/artifact-1/content?tenantId=tenant-a',
  ])
  for (const [, init] of fetchMock.mock.calls) {
    expect(init?.method ?? 'GET').toBe('GET')
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
  }
  fireEvent.click(screen.getByRole('button', { name: '关闭审阅记录' }))
  expect(screen.queryByText('Restore the zero guard')).not.toBeInTheDocument()
})

it.each([
  [401, '身份已失效'], [403, '当前身份无权'], [404, '未找到可访问'], [500, '请求未完成'],
])('shows a safe error for HTTP %i without displaying upstream content', async (status, message) => {
  const fetchMock = vi.fn().mockResolvedValue(Response.json({ error: 'unknown', message: 'SECRET_UPSTREAM_BODY' }, { status }))
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><ReviewRecord task={reviewTask} reference={reference} /></APIContext.Provider>)
  expect(await screen.findByRole('alert')).toHaveTextContent(message)
  expect(screen.queryByText(/SECRET_UPSTREAM_BODY/)).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

it.each([
  ['execution', { id: 'review-2' }],
  ['tenant', { tenantId: 'tenant-b' }],
  ['task', { taskId: 'task-2' }],
  ['workspace', { workspaceId: 'workspace-2' }],
  ['workspace version', { workspaceVersion: 4 }],
  ['fixed head', { headSha: '3'.repeat(40) }],
  ['artifact', { artifact: { ...first.artifact, id: 'artifact-2' } }],
])('rejects a mismatched %s before offering any Findings read', async (_name, change) => {
  const fetchMock = vi.fn().mockResolvedValue(Response.json({ ...first, ...change }))
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><ReviewRecord task={reviewTask} reference={reference} /></APIContext.Provider>)
  expect(await screen.findByRole('alert')).toHaveTextContent(/不一致/)
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

it.each(['identity', 'reference'])('rejects an invalid %s before sending a request', async (invalid) => {
  const fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><ReviewRecord
    task={invalid === 'identity' ? { ...reviewTask, tenantId: 'tenant-b' } : reviewTask}
    reference={invalid === 'reference' ? { ...reference, workspaceVersion: 0 } : reference} /></APIContext.Provider>)
  await screen.findByRole('alert')
  expect(fetchMock).not.toHaveBeenCalled()
})

it('refreshes a started execution to its terminal state using only reads', async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(reviewExecution('RUNNING')))
    .mockResolvedValueOnce(Response.json(first))
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><ReviewRecord task={reviewTask} reference={{ ...reference, artifactId: undefined }} /></APIContext.Provider>)
  await screen.findByText('审阅进行中')
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '刷新执行记录' }))
  await screen.findByText('本次审阅成功')
  expect(screen.getByRole('button', { name: '读取审阅结果' })).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(2)
  for (const [path, init] of fetchMock.mock.calls) {
    expect(path).toBe('/api/v1/tasks/task-1/reviews/review-1?tenantId=tenant-a')
    expect(init?.method ?? 'GET').toBe('GET')
  }
})

it.each([
  ['FAILED', 'review_findings_invalid', '模型输出未通过校验'],
  ['CANCELED', 'review_canceled', '审阅已取消'],
])('shows a %s record without exposing a Findings action', async (state, errorCode, message) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ ...reviewExecution(state), errorCode })))
  render(<APIContext.Provider value={api}><ReviewRecord task={reviewTask} reference={{ ...reference, artifactId: undefined }} /></APIContext.Provider>)
  await screen.findByText(new RegExp(`^${message}`))
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
})

it('ignores a late JSON body from the previously selected execution', async () => {
  let finishBody!: (body: unknown) => void
  const body = new Promise<unknown>((resolve) => { finishBody = resolve })
  const readBody = vi.fn(() => body)
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ items: events }))
    .mockResolvedValueOnce({ ok: true, json: readBody })
    .mockResolvedValueOnce(Response.json({ ...reviewExecution('FAILED', 'review-2'), errorCode: 'review_findings_invalid' }))
  vi.stubGlobal('fetch', fetchMock)
  render(<APIContext.Provider value={api}><TaskEventTimeline task={reviewTask} /></APIContext.Provider>)
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))
  fireEvent.click(await screen.findByRole('button', { name: '查看执行 review-1（事件 6）' }))
  await waitFor(() => expect(readBody).toHaveBeenCalledTimes(1))
  const oldSignal = fetchMock.mock.calls[1][1].signal as AbortSignal
  fireEvent.click(screen.getByRole('button', { name: '查看执行 review-2（事件 8）' }))
  await screen.findByText('本次审阅失败')
  expect(oldSignal.aborted).toBe(true)
  await act(async () => finishBody(first))
  expect(screen.queryByRole('region', { name: '执行 review-1 的记录' })).not.toBeInTheDocument()
  expect(screen.queryByText('本次审阅成功')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
})

it.each(['close', 'task', 'tenant'])('discards pending Artifact content on %s change', async (change) => {
  const content = deferredResponse()
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ items: events }))
    .mockResolvedValueOnce(Response.json(first)).mockReturnValueOnce(content.promise)
  vi.stubGlobal('fetch', fetchMock)
  const view = render(<APIContext.Provider value={api}><TaskEventTimeline task={reviewTask} /></APIContext.Provider>)
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))
  fireEvent.click(await screen.findByRole('button', { name: '查看执行 review-1（事件 6）' }))
  fireEvent.click(await screen.findByRole('button', { name: '读取审阅结果' }))
  expect(fetchMock).toHaveBeenCalledTimes(3)
  const signal = fetchMock.mock.calls[2][1].signal as AbortSignal
  if (change === 'close') fireEvent.click(screen.getByRole('button', { name: '关闭审阅记录' }))
  else view.rerender(<APIContext.Provider value={change === 'tenant' ? { ...api, tenantId: 'tenant-b' } : api}>
    <TaskEventTimeline task={{ ...reviewTask, ...(change === 'task' ? { id: 'task-2' } : { tenantId: 'tenant-b' }) }} />
  </APIContext.Provider>)
  expect(signal.aborted).toBe(true)
  await act(async () => content.resolve(new Response(findingsText)))
  expect(screen.queryByText('Restore the zero guard')).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: '执行 review-1 的记录' })).not.toBeInTheDocument()
})
