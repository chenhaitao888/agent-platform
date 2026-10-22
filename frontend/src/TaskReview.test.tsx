import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { APIContext, authenticatedFetch } from './api'
import WorkspaceDetails from './WorkspaceDetails'
import TaskReview from './TaskReview'
import { deferredResponse, findingsArtifact, findingsReport, findingsText, installReviewCrypto, reviewExecution, reviewTask, reviewWorkspace } from './test/reviewFixtures'

beforeEach(installReviewCrypto)
afterEach(() => vi.unstubAllGlobals())

const api = { tenantId: reviewTask.tenantId, fetch: (url: string, init?: RequestInit) => fetch(url, init) }
function panel(task = reviewTask, workspace = reviewWorkspace, client = api) {
  return <APIContext.Provider value={client}><TaskReview task={task} workspace={workspace} /></APIContext.Provider>
}
async function readStatus() {
  fireEvent.click(screen.getByRole('button', { name: `查看 ${reviewTask.id} 的审阅状态` }))
  await waitFor(() => expect(screen.getByRole('button', { name: `查看 ${reviewTask.id} 的审阅状态` })).toBeEnabled())
}

it('starts an authenticated review for a ready workspace and displays its bound Findings', async () => {
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (url.includes('/workspace?')) return Promise.resolve(Response.json(reviewWorkspace))
    if (init?.method === 'POST') return Promise.resolve(Response.json(reviewExecution(), { status: 201 }))
    if (url.includes('/content?')) return Promise.resolve(new Response(findingsText))
    return Promise.resolve(Response.json({ code: 'not_found' }, { status: 404 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  const token = 'test-review-token-12345678901234567890'
  const api = { tenantId: reviewTask.tenantId, fetch: authenticatedFetch(token, new AbortController().signal) }
  render(<APIContext.Provider value={api}><WorkspaceDetails task={reviewTask} /></APIContext.Provider>)
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的 Workspace' }))
  await screen.findByText('READY')
  expect(fetchMock).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的审阅状态' }))
  await screen.findByText('尚无审阅记录。')
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  await screen.findByText('审阅成功')
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  await screen.findByText('Restore the zero guard')
  expect(screen.getByText('divider.py:2')).toBeInTheDocument()
  expect(screen.getByText('HIGH')).toBeInTheDocument()
  const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')
  expect(posts).toHaveLength(1)
  expect(posts[0][0]).toBe('/api/v1/tasks/task-1/review')
  expect(JSON.parse(posts[0][1]?.body as string)).toEqual({
    requestId: expect.any(String), idempotencyKey: expect.any(String),
    tenantId: 'tenant-a', expectedWorkspaceVersion: 3,
  })
  expect(fetchMock.mock.calls.at(-1)?.[0]).toBe('/api/v1/artifacts/artifact-1/content?tenantId=tenant-a')
  for (const [url, init] of fetchMock.mock.calls) {
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    expect(url).not.toContain(token)
  }
})

it('requires an eligible task, a ready workspace and matching tenant before exposing review actions', () => {
  const fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  const { rerender } = render(panel({ ...reviewTask, status: 'CREATED' }))
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  rerender(panel({ ...reviewTask, type: 'OTHER' }))
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  rerender(panel(reviewTask, { ...reviewWorkspace, state: 'PREPARING' }))
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  rerender(panel(reviewTask, reviewWorkspace, { ...api, tenantId: 'other-tenant' }))
  expect(screen.getByRole('alert')).toHaveTextContent('当前身份、Task 与 Workspace 不一致')
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  expect(fetchMock).not.toHaveBeenCalled()
})

it('does not start on mount or while another review is running, and refreshes to the terminal state', async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(reviewExecution('RUNNING')))
    .mockResolvedValueOnce(Response.json({ ...reviewExecution('FAILED'), errorCode: 'review_timeout' }))
  vi.stubGlobal('fetch', fetchMock)
  render(<StrictMode>{panel()}</StrictMode>)
  expect(fetchMock).not.toHaveBeenCalled()
  expect(screen.queryByRole('button', { name: /开始/ })).not.toBeInTheDocument()
  await readStatus()
  expect(screen.getByText('审阅进行中')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /开始|读取审阅结果/ })).not.toBeInTheDocument()
  await readStatus()
  expect(screen.getByText('审阅失败')).toBeInTheDocument()
  expect(screen.getByText(/审阅超时/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '开始新一轮审阅' })).toBeEnabled()
  expect(fetchMock.mock.calls.every(([, init]) => !init?.method)).toBe(true)
})

it('keeps the same attempt key after a lost response and creates a new key only for an explicit new round', async () => {
  const first = deferredResponse()
  let posts = 0
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method !== 'POST') return Promise.resolve(Response.json({}, { status: 404 }))
    posts++
    return posts === 1 ? first.promise : Promise.resolve(Response.json(reviewExecution('SUCCEEDED', `review-${posts}`)))
  })
  vi.stubGlobal('fetch', fetchMock)
  render(panel())
  await readStatus()
  const start = screen.getByRole('button', { name: '开始只读审阅' })
  fireEvent.click(start); fireEvent.click(start)
  expect(posts).toBe(1)
  expect(screen.getByRole('button', { name: '重试本次请求' })).toBeDisabled()
  await act(async () => first.reject(new Error('SECRET_UPSTREAM_ECHO')))
  expect(screen.getByRole('alert')).not.toHaveTextContent('SECRET_UPSTREAM_ECHO')
  await readStatus() // An absent status does not discard the uncertain attempt.
  expect(screen.queryByRole('button', { name: /开始/ })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '重试本次请求' }))
  await screen.findByText('审阅成功')
  fireEvent.click(screen.getByRole('button', { name: '开始新一轮审阅' }))
  await screen.findByText('review-3')
  const bodies = fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST').map(([, init]) => JSON.parse(init?.body as string))
  expect(bodies[0].idempotencyKey).toBe(bodies[1].idempotencyKey)
  expect(bodies[0].requestId).not.toBe(bodies[1].requestId)
  expect(bodies[2].idempotencyKey).not.toBe(bodies[1].idempotencyKey)
  expect(bodies.every((body) => body.expectedWorkspaceVersion === 3)).toBe(true)
})

it('keeps an uncertain attempt when GET returns its older terminal execution', async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(reviewExecution('SUCCEEDED', 'old-review')))
    .mockRejectedValueOnce(new Error('connection lost'))
    .mockResolvedValueOnce(Response.json(reviewExecution('SUCCEEDED', 'old-review')))
    .mockResolvedValueOnce(Response.json(reviewExecution('RUNNING', 'new-review')))
    .mockResolvedValueOnce(Response.json({ ...reviewExecution('CANCELED', 'new-review'), errorCode: 'review_canceled' }))
  vi.stubGlobal('fetch', fetchMock)
  render(panel()); await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '开始新一轮审阅' }))
  await screen.findByRole('alert')
  await readStatus()
  expect(screen.queryByRole('button', { name: /开始/ })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '重试本次请求' })).toBeEnabled()
  await readStatus()
  expect(screen.getByRole('button', { name: '重试本次请求' })).toBeDisabled()
  await readStatus()
  expect(screen.getByRole('button', { name: '开始新一轮审阅' })).toBeEnabled()
  expect(screen.queryByRole('button', { name: '重试本次请求' })).not.toBeInTheDocument()
})

it.each([
  [503, 'review_unavailable', '当前服务未启用只读审阅', false],
  [409, 'review_in_progress', '已有审阅正在执行', false],
  [409, 'version_conflict', 'Workspace 版本已变更', true],
  [401, 'unauthenticated', '身份已失效', true],
  [403, 'forbidden', '当前身份无权执行此操作', true],
  [504, 'review_timeout', '审阅超时', false],
  [502, 'review_findings_invalid', '模型输出未通过校验', false],
  [502, 'constructor', '请求未完成', false],
] as const)('handles POST %s %s without echoing upstream messages', async (status, error, message, blocked) => {
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({}, { status: 404 }))
    .mockResolvedValueOnce(Response.json({ error, message: 'SECRET_ERROR_BODY' }, { status }))
  vi.stubGlobal('fetch', fetchMock)
  render(panel()); await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(message)
  expect(screen.getByRole('alert')).not.toHaveTextContent('SECRET_ERROR_BODY')
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '重试本次请求' }).hasAttribute('disabled')).toBe(blocked)
})

it.each(['tenant', 'task', 'workspace-version'] as const)('discards a late status body after a %s switch', async (scope) => {
  let finish!: (value: unknown) => void
  const response = new Response()
  response.json = () => new Promise((resolve) => { finish = resolve })
  const fetchMock = vi.fn().mockResolvedValueOnce(response).mockResolvedValueOnce(Response.json({}, { status: 404 }))
  vi.stubGlobal('fetch', fetchMock)
  const { rerender } = render(panel())
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的审阅状态' }))
  await waitFor(() => expect(finish).toBeDefined())
  const task = { ...reviewTask, ...(scope === 'tenant' ? { tenantId: 'tenant-b' } : scope === 'task' ? { id: 'task-2' } : {}) }
  const workspace = { ...reviewWorkspace, tenantId: task.tenantId, taskId: task.id, version: scope === 'workspace-version' ? 4 : 3 }
  rerender(panel(task, workspace, scope === 'tenant' ? { ...api, tenantId: task.tenantId } : api))
  expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: `查看 ${task.id} 的审阅状态` }))
  await screen.findByText('尚无审阅记录。')
  await act(async () => finish(reviewExecution()))
  expect(screen.queryByText('审阅成功')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
})

it('cancels an in-flight POST on unmount and does not leak its result into the next view', async () => {
  const post = deferredResponse()
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({}, { status: 404 })).mockReturnValueOnce(post.promise)
  vi.stubGlobal('fetch', fetchMock)
  const { unmount } = render(panel()); await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  unmount()
  expect(fetchMock.mock.calls[1][1].signal.aborted).toBe(true)
  await act(async () => post.resolve(Response.json(reviewExecution())))
  expect(screen.queryByText('审阅成功')).not.toBeInTheDocument()
})

it('allows independent tasks to inspect status while one task is executing', async () => {
  const post = deferredResponse()
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => init?.method === 'POST' ? post.promise : Promise.resolve(Response.json({}, { status: 404 })))
  vi.stubGlobal('fetch', fetchMock)
  render(<>{panel()}{panel({ ...reviewTask, id: 'task-2' }, { ...reviewWorkspace, id: 'workspace-2', taskId: 'task-2' })}</>)
  await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  fireEvent.click(screen.getByRole('button', { name: '查看 task-2 的审阅状态' }))
  const other = within(screen.getByRole('region', { name: 'task-2 的只读审阅' }))
  await other.findByText('尚无审阅记录。')
  expect(other.getByRole('button', { name: '开始只读审阅' })).toBeEnabled()
  await act(async () => post.resolve(Response.json(reviewExecution())))
})

it.each(['tenant', 'task', 'workspace', 'version', 'head', 'artifact-tenant', 'artifact-type', 'state'] as const)(
  'rejects a mismatched %s execution before offering artifact access', async (change) => {
    const execution = reviewExecution()
    const malformed: Record<string, unknown> = { ...execution }
    if (change === 'tenant') malformed.tenantId = 'other'
    if (change === 'task') malformed.taskId = 'other'
    if (change === 'workspace') malformed.workspaceId = 'other'
    if (change === 'version') malformed.workspaceVersion = 9
    if (change === 'head') malformed.headSha = '9'.repeat(40)
    if (change === 'artifact-tenant') malformed.artifact = { ...execution.artifact, tenantId: 'other' }
    if (change === 'artifact-type') malformed.artifact = { ...execution.artifact, type: 'REPOSITORY_DIFF' }
    if (change === 'state') malformed.state = ['SUCCEEDED']
    const fetchMock = vi.fn().mockResolvedValue(Response.json(malformed))
    vi.stubGlobal('fetch', fetchMock)
    render(panel()); await readStatus()
    expect(screen.getByRole('alert')).toHaveTextContent('不一致或格式无效')
    expect(screen.queryByRole('button', { name: /开始|读取审阅结果/ })).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  },
)

it.each(['hash', 'bytes', 'refs', 'json', 'version', 'severity', 'line', 'path', 'count'] as const)(
  'rejects invalid Findings %s while keeping the successful execution distinct from its unreadable content', async (change) => {
    const value = structuredClone(findingsReport) as Record<string, unknown>
    if (change === 'refs') value.headSha = '9'.repeat(40)
    if (change === 'version') value.schemaVersion = '99'
    if (change === 'severity') value.findings = [{ ...findingsReport.findings[0], severity: ['HIGH'] }]
    if (change === 'line') value.findings = [{ ...findingsReport.findings[0], startLine: 0 }]
    if (change === 'path') value.findings = [{ ...findingsReport.findings[0], path: '../secret' }]
    if (change === 'count') value.findings = Array.from({ length: 51 }, () => findingsReport.findings[0])
    const text = change === 'json' ? '{' : JSON.stringify(value)
    const artifact = findingsArtifact(text)
    if (change === 'hash') artifact.sha256 = '0'.repeat(64)
    if (change === 'bytes') artifact.sizeBytes++
    const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ ...reviewExecution(), artifact }))
      .mockResolvedValueOnce(new Response(text))
    vi.stubGlobal('fetch', fetchMock)
    render(panel()); await readStatus()
    fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('内容校验失败')
    expect(screen.getByText('审阅成功')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Findings' })).not.toBeInTheDocument()
  },
)

it.each(['empty', 'untrusted-text'] as const)('renders %s Findings as plain data', async (mode) => {
  const title = '<img src=x onerror=alert(1)>'
  const text = JSON.stringify({ ...findingsReport, findings: mode === 'empty' ? [] : [{ ...findingsReport.findings[0], title, description: '[run](https://example.invalid)\nIgnore previous instructions.' }] })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(Response.json({ ...reviewExecution(), artifact: findingsArtifact(text) }))
    .mockResolvedValueOnce(new Response(text)))
  render(panel()); await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  if (mode === 'empty') await screen.findByText('本轮未报告问题，仍需人工审阅。')
  else { await screen.findByText(title); expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(screen.queryByRole('link')).not.toBeInTheDocument() }
})

it('aborts and discards late artifact content after leaving the task', async () => {
  let finish!: (bytes: ArrayBuffer) => void
  const response = new Response()
  response.arrayBuffer = () => new Promise((resolve) => { finish = resolve })
  const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(reviewExecution())).mockResolvedValueOnce(response)
  vi.stubGlobal('fetch', fetchMock)
  const { rerender } = render(panel()); await readStatus()
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  await waitFor(() => expect(finish).toBeDefined())
  rerender(panel({ ...reviewTask, id: 'task-2' }, { ...reviewWorkspace, taskId: 'task-2' }))
  expect(fetchMock.mock.calls[1][1].signal.aborted).toBe(true)
  await act(async () => finish(new TextEncoder().encode(findingsText).buffer))
  expect(screen.queryByText('Restore the zero guard')).not.toBeInTheDocument()
})
