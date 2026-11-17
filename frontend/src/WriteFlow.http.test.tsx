import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_WRITE_URL
afterEach(() => vi.unstubAllGlobals())

it.skipIf(!endpoint)('approves and executes reference config/AB writes through the real API and dedicated Temporal Worker', async () => {
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  const unknown = env.AGENT_PLATFORM_WRITE_SCENARIO === 'unknown'
  const starts: string[] = []; const posts: { path: string; body: string }[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    if (init?.method === 'POST') posts.push({ path, body: init.body as string })
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (init?.method === 'POST' && path.endsWith('/write-executions')) {
      starts.push(init.body as string)
      if (starts.length === 1 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled lost API start response') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  fireEvent.click(await screen.findByRole('button', { name: '交付包' }))
  fireEvent.click(await screen.findByRole('button', { name: new RegExp(env.AGENT_PLATFORM_WRITE_BUNDLE!) }))
  const panel = await screen.findByRole('region', { name: '配置与实验写入' })
  await waitFor(() => expect(within(panel).getByRole('button', { name: '批准写入' })).toBeEnabled())
  expect(posts).toHaveLength(0)
  fireEvent.change(within(panel).getByLabelText('写入批准理由'), { target: { value: '已复核配置业务值和未激活实验定义' } })
  fireEvent.click(within(panel).getByLabelText('已复核 Findings、配置值和实验定义，批准本交付包的写入'))
  fireEvent.click(within(panel).getByRole('button', { name: '批准写入' }))
  await within(panel).findByText(/fixture-user · 可用于写入/)
  fireEvent.click(within(panel).getByLabelText('确认执行 test 环境的配置与实验草稿变更'))
  fireEvent.click(within(panel).getByRole('button', { name: '执行配置与实验写入' }))
  fireEvent.click(await within(panel).findByRole('button', { name: '重试本次写入操作' }))
  await waitFor(() => expect(starts).toHaveLength(2))
  expect(starts[0]).toBe(starts[1])
  if (unknown) {
    await within(panel).findByRole('heading', { name: '结果待核对' }, { timeout: 30000 })
    fireEvent.change(within(panel).getByLabelText('写入撤回理由'), { target: { value: '停止剩余操作，仅核对已有结果' } })
    fireEvent.click(within(panel).getByRole('button', { name: /撤回写入批准：/ }))
    await within(panel).findByText(/fixture-user · 已撤回/)
    fireEvent.click(within(panel).getByRole('button', { name: /只读核对结果：/ }))
    await within(panel).findByRole('heading', { name: '已停止' }, { timeout: 15000 })
    expect(within(panel).getAllByText('已写入并核验')).toHaveLength(1)
    expect(posts.find(p => p.path.endsWith('/reconcile'))?.body).toBe('{}')
  } else {
    await within(panel).findByRole('heading', { name: '执行完成 · 已逐项核验' }, { timeout: 30000 })
    expect(within(panel).getAllByText('已写入并核验')).toHaveLength(3)
    expect(within(panel).getAllByText(/保存后仍需发布/).length).toBeGreaterThan(0)
  }
  expect(posts.every(p => p.path.includes('/write-'))).toBe(true)
  expect(posts.map(p => p.body).join('')).not.toMatch(/intent|credential|connectorRef|scenario|payload|secret/)
  fireEvent.click(within(panel).getByRole('button', { name: '刷新写入记录' }))
  await waitFor(() => expect(within(panel).queryByText('正在读取写入状态…')).not.toBeInTheDocument())
  expect(starts).toHaveLength(2)
}, 60000)
