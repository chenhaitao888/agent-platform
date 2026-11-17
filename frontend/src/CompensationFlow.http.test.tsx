import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_COMPENSATION_URL
afterEach(() => vi.unstubAllGlobals())

it.skipIf(!endpoint)('prepares a receipt-bound inverse, preserves manual items and requires independent compensation approval', async () => {
  const nativeFetch = globalThis.fetch
  const preparations: string[] = []; const posts: string[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push(path)
    const response = await nativeFetch(`${endpoint}${path}`, init)
    if (init?.method === 'POST' && path.endsWith('/compensations')) {
      preparations.push(init.body as string)
      if (preparations.length === 1 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled lost preparation reply') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: 'api-test-token-with-at-least-32-characters' } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  fireEvent.click(await screen.findByRole('button', { name: '交付包' }))
  fireEvent.click(await screen.findByRole('button', { name: new RegExp(env.AGENT_PLATFORM_COMPENSATION_BUNDLE!) }))
  const preparation = await screen.findByRole('region', { name: '处置与补偿计划' })
  await waitFor(() => expect(within(preparation).getByRole('button', { name: '准备处置与补偿计划' })).toBeEnabled())
  expect(posts).toHaveLength(0)
  fireEvent.change(within(preparation).getByLabelText('处置理由'), { target: { value: '恢复原超时值，新增配置和草稿保留为人工事项' } })
  fireEvent.click(within(preparation).getByRole('button', { name: '准备处置与补偿计划' }))
  fireEvent.click(await within(preparation).findByRole('button', { name: '重试本次补偿准备' }))
  const open = await within(preparation).findByRole('button', { name: /打开补偿交付包：/ })
  expect(preparations[0]).toBe(preparations[1])
  expect(within(preparation).getAllByText(/· 人工处置/)).toHaveLength(2)
  expect(within(preparation).getByText('当前值：200 → 恢复值：100')).toBeInTheDocument()
  fireEvent.click(open)
  const execution = await screen.findByRole('region', { name: '配置补偿执行' })
  expect(await screen.findByText('仍有 2 项人工处置。本包完成不代表整体回滚完成。')).toBeInTheDocument()
  await waitFor(() => expect(within(execution).getByRole('button', { name: '批准补偿' })).toBeEnabled())
  expect(within(execution).getByRole('button', { name: '执行配置补偿' })).toBeDisabled()
  fireEvent.change(within(execution).getByLabelText('写入批准理由'), { target: { value: '已复核当前版本与反向配置值' } })
  fireEvent.click(within(execution).getByLabelText('已复核原回执、补偿值和人工事项，批准本补偿计划'))
  fireEvent.click(within(execution).getByRole('button', { name: '批准补偿' }))
  await within(execution).findByText(/fixture-user · 可用于写入/)
  fireEvent.click(within(execution).getByLabelText('确认执行 test 环境的配置补偿'))
  fireEvent.click(within(execution).getByRole('button', { name: '执行配置补偿' }))
  await within(execution).findByRole('heading', { name: '补偿动作完成 · 已逐项核验' }, { timeout: 30000 })
  expect(posts.filter(p => p.endsWith('/write-approvals'))).toHaveLength(1)
  expect(posts.filter(p => p.endsWith('/write-executions'))).toHaveLength(1)
  expect(preparations.join('')).not.toMatch(/restoreValue|writer|intent|secret|payload/)
  fireEvent.click(screen.getByRole('button', { name: '查看原交付包' }))
  await screen.findByRole('region', { name: '配置与实验写入' })
  expect(await screen.findByRole('heading', { name: '执行完成 · 已逐项核验' })).toBeInTheDocument()
}, 60000)
