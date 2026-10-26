import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { ReviewExecution } from './review'
import { installReviewCrypto } from './test/reviewFixtures'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_FRONTEND_TEST_URL
afterEach(() => vi.unstubAllGlobals())

it.skipIf(!endpoint)('reviews through the real authenticated Go API, replays a lost response and isolates tenants', async () => {
  expect(new URL(endpoint!).hostname).toBe('127.0.0.1')
  installReviewCrypto()
  const originalFetch = globalThis.fetch
  const tokenA = 'tenant-a-test-token-12345678901234567890'
  const tokenB = 'tenant-b-test-token-12345678901234567890'
  const posts: Record<string, unknown>[] = []
  const reads: string[] = []
  let loseFirstResponse = true
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(path.startsWith('/api/v1/')).toBe(true)
    expect(new Headers(init?.headers).has('Authorization')).toBe(true)
    if ((init?.method ?? 'GET') === 'GET') reads.push(path)
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (path.endsWith('/review') && init?.method === 'POST') {
      posts.push(JSON.parse(init.body as string))
      if (loseFirstResponse) {
        loseFirstResponse = false
        expect(response.status).toBe(201)
        await response.arrayBuffer()
        throw new TypeError('controlled response loss after the server committed')
      }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: tokenA } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('Review feature')
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的 Workspace' }))
  await screen.findByText('READY')
  // Native HTTP can commit the new panel before its passive effects flush.
  // Finish mounting it before simulating the next user interaction.
  await act(async () => {})
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的审阅状态' }))
  await screen.findByText('尚无审阅记录。')
  fireEvent.click(screen.getByRole('button', { name: '开始只读审阅' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('请求结果未确认')
  fireEvent.click(screen.getByRole('button', { name: '重试本次请求' }))
  await screen.findByText('审阅成功')
  expect(posts).toHaveLength(2)
  expect(posts[0].idempotencyKey).toBe(posts[1].idempotencyKey)
  expect(posts[0].tenantId).toBe('tenant-review')
  expect(posts[0].expectedWorkspaceVersion).toBe(3)
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  await screen.findByText('Unchecked error')
  expect(screen.getByText('feature.txt:1')).toBeInTheDocument()

  const latestURL = `${endpoint}/api/v1/tasks/task-1/review?tenantId=tenant-review`
  const headersA = { Authorization: `Bearer ${tokenA}` }
  const previous = await (await originalFetch(latestURL, { headers: headersA })).json() as ReviewExecution
  fireEvent.click(screen.getByRole('button', { name: '开始新一轮审阅' }))
  await screen.findByText('审阅成功')
  expect(posts).toHaveLength(3)
  expect(posts[2].idempotencyKey).not.toBe(posts[1].idempotencyKey)
  const current = await (await originalFetch(latestURL, { headers: headersA })).json() as ReviewExecution
  expect(current.id).not.toBe(previous.id)
  expect(current.artifact!.id).not.toBe(previous.artifact!.id)
  fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))
  fireEvent.click(await screen.findByRole('button', { name: `查看执行 ${previous.id}（事件 8）` }))
  const record = screen.getByRole('region', { name: `执行 ${previous.id} 的记录` })
  fireEvent.click(await within(record).findByRole('button', { name: '读取审阅结果' }))
  await within(record).findByText('Unchecked error')
  expect(reads).toContain(`/api/v1/tasks/task-1/reviews/${previous.id}?tenantId=tenant-review`)
  expect(reads.at(-1)).toBe(`/api/v1/artifacts/${previous.artifact!.id}/content?tenantId=tenant-review`)
  expect(posts).toHaveLength(3)
  expect((await (await originalFetch(latestURL, { headers: headersA })).json() as ReviewExecution).id).toBe(current.id)
  expect((await originalFetch(latestURL)).status).toBe(401)
  expect((await originalFetch(latestURL, { headers: { Authorization: `Bearer ${tokenB}` } })).status).toBe(403)
  expect((await originalFetch(`${endpoint}/api/v1/tasks/task-1/review?tenantId=tenant-b`, { headers: { Authorization: `Bearer ${tokenB}` } })).status).toBe(404)
  expect((await originalFetch(`${endpoint}/api/v1/artifacts/${current.artifact!.id}/content?tenantId=tenant-b`, { headers: { Authorization: `Bearer ${tokenB}` } })).status).toBe(404)
  expect((await originalFetch(`${endpoint}/api/v1/tasks/task-1/reviews/${previous.id}?tenantId=tenant-b`, { headers: { Authorization: `Bearer ${tokenB}` } })).status).toBe(404)
  expect((await originalFetch(`${endpoint}/api/v1/artifacts/${previous.artifact!.id}/content?tenantId=tenant-b`, { headers: { Authorization: `Bearer ${tokenB}` } })).status).toBe(404)

  fireEvent.click(screen.getByRole('button', { name: '退出身份' }))
  expect(screen.queryByText('审阅成功')).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: `执行 ${previous.id} 的记录` })).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: tokenB } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无 Task')
  expect(screen.queryByText('Unchecked error')).not.toBeInTheDocument()
  expect(localStorage.length).toBe(0)
  expect(sessionStorage.length).toBe(0)
}, 20_000)
