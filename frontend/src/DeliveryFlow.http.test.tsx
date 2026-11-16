import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { DeliveryBundle } from './delivery'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_DELIVERY_URL
const { writeFileSync } = await vi.importActual<{ writeFileSync: (path: string, data: string) => void }>('node:fs')
afterEach(() => vi.unstubAllGlobals())
it.skipIf(!endpoint)('saves a plan package and reads its own archived Findings after an API restart', async () => {
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  const title = env.AGENT_PLATFORM_DELIVERY_TITLE!
  const phase = env.AGENT_PLATFORM_DELIVERY_PHASE
  const bodies: string[] = []
  const calls: { path: string; method?: string }[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method })
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (path.endsWith('/archive') && init?.method === 'POST' && response.ok) {
      bodies.push(init.body as string)
      const bundle = await response.clone().json() as DeliveryBundle
      writeFileSync(env.AGENT_PLATFORM_DELIVERY_RECEIPT!, JSON.stringify({ body: init.body, id: bundle.id, digest: bundle.digest }))
      if (bodies.length === 1) { await response.arrayBuffer(); throw new TypeError('controlled lost save response') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无需求')
  if (phase === 'save') {
    fireEvent.change(screen.getByLabelText('需求名称或编号'), { target: { value: title } })
    fireEvent.click(screen.getByLabelText('选择 S1'))
    fireEvent.change(screen.getByLabelText('S1 源分支'), { target: { value: 'feature' } })
    fireEvent.click(screen.getByRole('button', { name: '创建需求' }))
    fireEvent.click(await screen.findByRole('button', { name: '固定代码快照' }))
    const review = await screen.findByRole('button', { name: '开始多服务审阅' })
    await waitFor(() => expect(review).toBeEnabled()); fireEvent.click(review)
    await screen.findByRole('heading', { name: '快照 1 · 已完成审阅' }, { timeout: 15000 })
    fireEvent.click(screen.getByText('S1 · 补充业务要求与覆盖依据'))
    fireEvent.click(screen.getByLabelText('S1 确认不涉及配置'))
    fireEvent.change(screen.getByLabelText('S1 配置不涉及依据'), { target: { value: '已核对需求与代码，此版本无配置变更' } })
    fireEvent.click(screen.getByLabelText('S1 确认不涉及实验'))
    fireEvent.change(screen.getByLabelText('S1 实验不涉及依据'), { target: { value: '已核对需求与代码，此版本无实验变更' } })
    fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
    await screen.findByRole('heading', { name: '计划已整理 · 待人工审阅' })
    fireEvent.click(screen.getByRole('button', { name: '保存当前计划为交付包' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('保存结果未确认')
    fireEvent.click(screen.getByRole('button', { name: '重试本次保存' }))
    await screen.findByText('已保存，可从“交付包”查看。保存不代表批准或执行。')
    expect(bodies[0]).toBe(bodies[1])
    expect(JSON.parse(bodies[0])).toEqual({ idempotencyKey: expect.any(String), expectedPlanHash: expect.stringMatching(/^[0-9a-f]{64}$/) })
  }
  fireEvent.click(screen.getByRole('button', { name: '交付包' }))
  fireEvent.click(await screen.findByRole('button', { name: new RegExp(`^${title} · delivery-`) }))
  await screen.findByRole('heading', { name: title })
  expect(screen.getByRole('heading', { name: '计划已整理 · 待人工审阅' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '读取审阅结果' }))
  await screen.findByText('Saved review finding')
  expect(calls.some(c => c.path.startsWith('/api/v1/deliveries/') && c.path.includes('/artifacts/'))).toBe(true)
  expect(calls.some(c => c.path.startsWith('/api/v1/artifacts/'))).toBe(false)
  if (phase === 'restore') expect(calls.filter(c => c.method === 'POST')).toHaveLength(0)
}, 30000)
