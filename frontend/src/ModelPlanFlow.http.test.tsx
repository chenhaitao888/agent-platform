import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_MODEL_PLAN_URL
afterEach(() => vi.unstubAllGlobals())

it.skipIf(!endpoint)('discovers a wrapper dependency and requires explicit values before a reviewable plan', async () => {
  const originalFetch = globalThis.fetch
  const posts: string[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (path.endsWith('/plans') && init?.method === 'POST') {
      posts.push(init.body as string)
      if (posts.length === 1 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled lost response') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: 'api-test-token-with-at-least-32-characters' } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无需求')
  fireEvent.change(screen.getByLabelText('需求名称或编号'), { target: { value: 'Checkout configuration dependency' } })
  fireEvent.click(screen.getByLabelText('选择 S1'))
  fireEvent.change(screen.getByLabelText('S1 源分支'), { target: { value: 'feature' } })
  fireEvent.click(screen.getByRole('button', { name: '创建需求' }))
  fireEvent.click(await screen.findByRole('button', { name: '固定代码快照' }))
  const review = await screen.findByRole('button', { name: '开始多服务审阅' })
  await waitFor(() => expect(review).toBeEnabled())
  fireEvent.click(review)
  await screen.findByRole('heading', { name: '快照 1 · 已完成审阅' }, { timeout: 15000 })
  expect(posts).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('计划结果未确认')
  fireEvent.click(screen.getByRole('button', { name: '重试本次计划请求' }))
  await screen.findByRole('heading', { name: '计划存在待补齐或阻断项' })
  expect(posts[0]).toBe(posts[1])
  expect(screen.getByText(/已结合模型与规则分析/)).toBeInTheDocument()
  expect(screen.getByText('配置候选：checkout.limit')).toBeInTheDocument()
  expect(screen.getByText('代码证据：feature.txt:2')).toBeInTheDocument()
  expect(screen.getByText(/配置期望值尚未确定/)).toBeInTheDocument()
  fireEvent.click(screen.getByText('S1 · 补充业务要求与覆盖依据'))
  fireEvent.click(screen.getByRole('button', { name: '添加配置要求：S1' }))
  for (const [name, value] of [['名称', 'checkout.limit'], ['类型', 'integer'], ['期望值', '20'], ['理由', 'Test acceptance limit']]) fireEvent.change(screen.getByLabelText(`S1 配置 1 ${name}`), { target: { value } })
  fireEvent.click(screen.getByLabelText('S1 确认不涉及实验'))
  fireEvent.change(screen.getByLabelText('S1 实验不涉及依据'), { target: { value: 'Reviewed feature has no experiment changes' } })
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  await screen.findByRole('heading', { name: '计划已整理 · 待人工审阅' })
  expect(screen.getByRole('region', { name: '新增配置 checkout.limit' })).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('计划历史'), { target: { value: 'plan-1' } })
  fireEvent.click(screen.getByRole('button', { name: '刷新计划历史' }))
  await screen.findByRole('heading', { name: '计划存在待补齐或阻断项' })
  expect(posts).toHaveLength(3)
}, 30000)
