import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_SIMULATION_URL
afterEach(() => vi.unstubAllGlobals())
it.skipIf(!endpoint)('approves only simulation, recovers a lost start response and displays reconciled receipts over real Temporal', async () => {
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  const starts: string[] = []
  let approvals = 0
  const calls: { path: string; method?: string }[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    calls.push({ path, method: init?.method })
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (init?.method === 'POST' && path.endsWith('simulation-approvals')) approvals++
    if (init?.method === 'POST' && path.endsWith('/simulations')) {
      starts.push(init.body as string)
      if (starts.length === 1 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled lost start reply') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  fireEvent.click(await screen.findByRole('button', { name: '交付包' }))
  fireEvent.click(await screen.findByRole('button', { name: new RegExp(env.AGENT_PLATFORM_SIMULATION_BUNDLE!) }))
  const panel = await screen.findByRole('region', { name: '交付模拟' })
  await waitFor(() => expect(within(panel).getByRole('button', { name: '批准模拟' })).toBeEnabled())
  expect(approvals).toBe(0); expect(starts).toHaveLength(0)
  fireEvent.change(screen.getByLabelText('模拟批准理由'), { target: { value: '已复核 Findings 和业务值，仅作演练' } })
  fireEvent.click(screen.getByLabelText('已核对 Findings 和变更计划，仅批准模拟'))
  fireEvent.click(within(panel).getByRole('button', { name: '批准模拟' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '发起模拟' })).toBeEnabled())
  fireEvent.change(screen.getByLabelText('演练场景'), { target: { value: 'response_loss' } })
  fireEvent.click(screen.getByRole('button', { name: '发起模拟' }))
  expect(await within(panel).findByRole('alert')).toHaveTextContent('结果未确认')
  fireEvent.click(screen.getByRole('button', { name: '重试本次模拟操作' }))
  await screen.findByRole('heading', { name: '模拟完成 · 未写入外部平台' }, { timeout: 30000 })
  expect(starts[0]).toBe(starts[1]); expect(approvals).toBe(1)
  expect(screen.getAllByText('已核对模拟回执')).toHaveLength(2)
  expect(calls.filter(c => c.method === 'POST').every(c => c.path.includes('/simulation'))).toBe(true)
  expect(starts.join('')).not.toMatch(/credential|payload|connectorRef|business-value/)
  fireEvent.click(screen.getByRole('button', { name: '刷新模拟记录' }))
  await screen.findByRole('heading', { name: '模拟完成 · 未写入外部平台' })
  expect(starts).toHaveLength(2)
}, 45000)
