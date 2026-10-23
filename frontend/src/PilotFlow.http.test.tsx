import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { Task, TaskEvent } from './task'
import type { ReviewExecution } from './review'
import { installReviewCrypto } from './test/reviewFixtures'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_PILOT_URL
afterEach(() => vi.unstubAllGlobals())

it.skipIf(!endpoint)('creates, queues, prepares and reviews a task through the deployed API', async () => {
  expect(new URL(endpoint!).hostname).toBe('127.0.0.1')
  expect(env.AGENT_PLATFORM_PILOT_HEAD).toMatch(/^[0-9a-f]{40}$/)
  const scenario = env.AGENT_PLATFORM_PILOT_SCENARIO
  expect(['success', 'response-loss', 'invalid-findings']).toContain(scenario)
  const loseResponses = scenario === 'response-loss'
  installReviewCrypto()
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  const otherToken = 'other-api-test-token-at-least-32-characters'
  const headers = { Authorization: `Bearer ${token}` }
  const calls: { path: string; method: string; body?: Record<string, unknown>; status: number }[] = []
  const losses = new Set(loseResponses ? [
    'POST /api/v1/tasks', 'PATCH /api/v1/tasks/task-1', 'POST /api/v1/tasks/task-1/workspace',
    'POST /api/v1/tasks/task-1/workspace/prepare', 'POST /api/v1/tasks/task-1/artifacts/diff', 'POST /api/v1/tasks/task-1/review',
  ] : [])
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(path.startsWith('/api/v1/')).toBe(true)
    expect(path).not.toContain(token)
    expect(new Headers(init?.headers).has('Authorization')).toBe(true)
    const response = await originalFetch(`${endpoint}${path}`, init)
    calls.push({ path, method: init?.method ?? 'GET', status: response.status,
      body: init?.body ? JSON.parse(init.body as string) : undefined })
    if (response.ok && losses.delete(`${init?.method} ${path}`)) {
      // The actual server has already committed. Only delivery to the UI fails.
      await response.arrayBuffer()
      throw new TypeError('controlled response loss after commit')
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无 Task')

  fireEvent.change(screen.getByLabelText('任务目标'), { target: { value: 'Pilot review from an empty store' } })
  fireEvent.change(screen.getByLabelText('仓库 ID'), { target: { value: '43' } })
  fireEvent.change(screen.getByLabelText('Head SHA'), { target: { value: env.AGENT_PLATFORM_PILOT_HEAD } })
  fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('repository is not authorized')
  expect(screen.getByText('暂无 Task')).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('仓库 ID'), { target: { value: '42' } })
  fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert')).toHaveTextContent('创建失败：controlled response loss')
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
  }
  await screen.findByRole('region', { name: '创建结果' })
  expect(within(screen.getByRole('list', { name: '最近 Task' })).getAllByRole('listitem')).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: '将 task-1 加入队列' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert')).toHaveTextContent('Task 更新失败：controlled response loss')
    fireEvent.click(screen.getByRole('button', { name: '将 task-1 加入队列' }))
  }
  await screen.findByText('QUEUED')
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的 Workspace' }))
  fireEvent.click(await screen.findByRole('button', { name: '登记 Workspace' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert')).toHaveTextContent('Workspace 操作失败：controlled response loss')
    fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的 Workspace' }))
  }
  await screen.findByText('REGISTERED')
  fireEvent.click(screen.getByRole('button', { name: '准备 Workspace' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert', {}, { timeout: 10_000 })).toHaveTextContent('Workspace 操作失败：controlled response loss')
    fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的 Workspace' }))
  }
  await screen.findByText('READY', {}, { timeout: 10_000 })

  fireEvent.click(screen.getByRole('button', { name: '查看固定版本差异' }))
  const diff = await screen.findByRole('region', { name: 'task-1 的固定版本差异' })
  expect(within(diff).getByText(/\+feature-only change/)).toBeInTheDocument()
  fireEvent.click(within(diff).getByRole('button', { name: '归档为 Artifact' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert')).toHaveTextContent('Artifact 归档失败：controlled response loss')
    fireEvent.click(within(diff).getByRole('button', { name: '归档为 Artifact' }))
  }
  const artifact = await screen.findByRole('region', { name: 'task-1 的 Diff Artifact' })
  fireEvent.click(within(artifact).getByRole('button', { name: '读取 Artifact 内容' }))
  expect(await within(artifact).findByText(/\+feature-only change/)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的审阅状态' }))
  await screen.findByText('尚无审阅记录。')
  expect(calls.filter((call) => call.path.endsWith('/review') && call.method === 'POST')).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  if (loseResponses) {
    expect(await screen.findByRole('alert', {}, { timeout: 10_000 })).toHaveTextContent('请求结果未确认')
    fireEvent.click(screen.getByRole('button', { name: '重试本次请求' }))
  }
  if (scenario === 'invalid-findings') {
    expect(await screen.findByRole('alert', {}, { timeout: 10_000 })).toHaveTextContent('模型输出未通过校验')
    expect(screen.queryByRole('button', { name: '读取审阅结果' })).not.toBeInTheDocument()
    // A failed attempt replays its error. It must not silently call the model again.
    fireEvent.click(screen.getByRole('button', { name: '重试本次请求' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('模型输出未通过校验')
    fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的审阅状态' }))
    await screen.findByText('审阅失败')
    const failed = await (await originalFetch(`${endpoint}/api/v1/tasks/task-1/review?tenantId=test`, { headers })).json() as ReviewExecution
    expect(failed.state).toBe('FAILED')
    expect(failed.artifact).toBeUndefined()
    fireEvent.click(screen.getByRole('button', { name: '开始新一轮审阅' }))
  }
  await screen.findByText('审阅成功', {}, { timeout: 10_000 })
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  if (scenario === 'success') await screen.findByText('本轮未报告问题，仍需人工审阅。')
  else {
    await screen.findByText('Controlled review finding')
    expect(screen.getByText('feature.txt:1')).toBeInTheDocument()
    expect(screen.getByText('MEDIUM')).toBeInTheDocument()
  }

  const tasks = await (await originalFetch(`${endpoint}/api/v1/tasks?tenantId=test`, { headers })).json() as { items: Task[] }
  expect(tasks.items).toHaveLength(1)
  expect(tasks.items[0]).toMatchObject({ id: 'task-1', tenantId: 'test', status: 'QUEUED', version: 2,
    repository: { repositoryId: '42', targetSha: env.AGENT_PLATFORM_PILOT_BASE, baseSha: env.AGENT_PLATFORM_PILOT_BASE, headSha: env.AGENT_PLATFORM_PILOT_HEAD } })
  const operation = await (await originalFetch(`${endpoint}/api/v1/tasks/task-1/review?tenantId=test`, { headers })).json() as ReviewExecution
  expect(operation).toMatchObject({ state: 'SUCCEEDED', tenantId: 'test', taskId: 'task-1', workspaceVersion: 3,
    baseSha: env.AGENT_PLATFORM_PILOT_BASE, headSha: env.AGENT_PLATFORM_PILOT_HEAD,
    artifact: { type: 'PR_REVIEW_FINDINGS', mediaType: 'application/json' } })
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))
  const timeline = await screen.findByRole('list', { name: 'task-1 的事件时间线' })
  const expectedEvents = ['task.created', 'task.queued', 'workspace.registered', 'workspace.preparing', 'workspace.ready',
    'artifact.created', ...(scenario === 'invalid-findings' ? ['review.started', 'review.failed'] : []),
    'review.started', 'artifact.created', 'review.succeeded']
  const events = await (await originalFetch(`${endpoint}/api/v1/tasks/task-1/events?tenantId=test`, { headers })).json() as { items: TaskEvent[] }
  expect(events.items.map((event) => event.eventType)).toEqual(expectedEvents)
  expect(events.items.map((event) => event.sequence)).toEqual(expectedEvents.map((_, index) => index + 1))
  expect(within(timeline).getAllByRole('listitem')).toHaveLength(expectedEvents.length)

  const writes = calls.filter((call) => call.method !== 'GET')
  expect(writes.every((call) => call.body?.tenantId === 'test')).toBe(true)
  expect(writes[0].status).toBe(403)
  expect(writes.slice(1).every((call) => [200, 201, ...(scenario === 'invalid-findings' ? [502] : [])].includes(call.status))).toBe(true)
  expect(writes.find((call) => call.path.endsWith('/review'))?.body?.expectedWorkspaceVersion).toBe(3)
  expect(losses.size).toBe(0)
  if (loseResponses) {
    for (const path of ['/api/v1/tasks', '/api/v1/tasks/task-1', '/api/v1/tasks/task-1/artifacts/diff', '/api/v1/tasks/task-1/review']) {
      const retries = writes.filter((call) => call.path === path && call.status !== 403)
      expect(retries).toHaveLength(2)
      expect(retries[0].body?.idempotencyKey).toBe(retries[1].body?.idempotencyKey)
      expect(retries[0].body?.requestId).not.toBe(retries[1].body?.requestId)
      expect(retries[1].status).toBe(200)
    }
    expect(writes.filter((call) => call.path.endsWith('/workspace') || call.path.endsWith('/prepare'))).toHaveLength(2)
  }
  if (scenario === 'invalid-findings') {
    const reviews = writes.filter((call) => call.path.endsWith('/review'))
    expect(reviews.map((call) => call.status)).toEqual([502, 502, 201])
    expect(reviews[0].body?.idempotencyKey).toBe(reviews[1].body?.idempotencyKey)
    expect(reviews[2].body?.idempotencyKey).not.toBe(reviews[1].body?.idempotencyKey)
  }

  fireEvent.click(screen.getByRole('button', { name: '退出身份' }))
  expect(screen.queryByText('审阅成功')).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: otherToken } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无 Task')
  expect(screen.queryByText('Pilot review from an empty store')).not.toBeInTheDocument()
  const otherHeaders = { Authorization: `Bearer ${otherToken}` }
  for (const path of ['/api/v1/tasks/task-1', '/api/v1/tasks/task-1/workspace', '/api/v1/tasks/task-1/review',
    `/api/v1/artifacts/${operation.artifact!.id}/content`]) {
    expect((await originalFetch(`${endpoint}${path}?tenantId=other`, { headers: otherHeaders })).status).toBe(404)
  }
  for (const [path, method, fields] of [
    ['/api/v1/tasks/task-1', 'PATCH', { expectedVersion: 2, status: 'QUEUED' }],
    ['/api/v1/tasks/task-1/workspace', 'POST', {}],
    ['/api/v1/tasks/task-1/workspace/prepare', 'POST', { expectedVersion: 3 }],
    ['/api/v1/tasks/task-1/artifacts/diff', 'POST', { expectedWorkspaceVersion: 3 }],
    ['/api/v1/tasks/task-1/review', 'POST', { expectedWorkspaceVersion: 3 }],
  ] as const) {
    const response = await originalFetch(`${endpoint}${path}`, { method, headers: { ...otherHeaders, 'Content-Type': 'application/json' },
      body: JSON.stringify({ requestId: `forbidden:${path}`, idempotencyKey: `forbidden:${path}`, tenantId: 'other', ...fields }) })
    expect(response.status).toBe(404)
    await response.arrayBuffer()
  }
  const impersonated = await originalFetch(`${endpoint}/api/v1/tasks`, { method: 'POST', headers: { ...otherHeaders, 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...writes.find((call) => call.status === 201 && call.path === '/api/v1/tasks')!.body, idempotencyKey: 'forbidden-create' }) })
  expect(impersonated.status).toBe(403)
  const afterForeignWrites = await (await originalFetch(`${endpoint}/api/v1/tasks/task-1/events?tenantId=test`, { headers })).json()
  expect(afterForeignWrites).toEqual(events)
  expect(localStorage.length).toBe(0)
  expect(sessionStorage.length).toBe(0)
}, 30_000)
