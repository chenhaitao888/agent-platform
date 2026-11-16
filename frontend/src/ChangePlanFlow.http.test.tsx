import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { ChangePlan } from './planning'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_PLAN_URL
afterEach(() => vi.unstubAllGlobals())
it.skipIf(!endpoint)('previews shared configuration and inactive experiments through the deployed read-only reference connectors', async () => {
  const originalFetch = globalThis.fetch
  const token = 'api-test-token-with-at-least-32-characters'
  const posts: string[] = []
  vi.stubGlobal('fetch', async (path: string, init?: RequestInit) => {
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    const response = await originalFetch(`${endpoint}${path}`, init)
    if (path.endsWith('/plans') && init?.method === 'POST') {
      posts.push(init.body as string)
      if (posts.length === 2 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled lost plan response') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByText('暂无需求')
  fireEvent.change(screen.getByLabelText('需求名称或编号'), { target: { value: '准备测试配置与实验' } })
  for (const id of ['S1', 'S2', 'S3']) {
    fireEvent.click(screen.getByLabelText(`选择 ${id}`))
    fireEvent.change(screen.getByLabelText(`${id} 源分支`), { target: { value: 'feature' } })
  }
  fireEvent.click(screen.getByRole('button', { name: '创建需求' }))
  fireEvent.click(await screen.findByRole('button', { name: '固定代码快照' }))
  const review = await screen.findByRole('button', { name: '开始多服务审阅' })
  await waitFor(() => expect(review).toBeEnabled())
  fireEvent.click(review)
  await screen.findByRole('heading', { name: '快照 1 · 已完成审阅' }, { timeout: 15000 })
  expect(posts).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  await screen.findByRole('heading', { name: '计划存在待补齐或阻断项' })
  expect(screen.getAllByText(/未发现候选，待确认覆盖/)).toHaveLength(3)
  for (const id of ['S1', 'S2']) {
    fireEvent.click(screen.getByText(`${id} · 补充业务要求与覆盖依据`))
    fireEvent.click(screen.getByRole('button', { name: `添加配置要求：${id}` }))
    for (const [name, value] of [['名称', 'feature.timeout'], ['类型', 'integer'], ['期望值', '200'], ['理由', '验收需要统一超时 200ms']]) fireEvent.change(screen.getByLabelText(`${id} 配置 1 ${name}`), { target: { value } })
    fireEvent.click(screen.getByLabelText(`${id} 确认不涉及实验`))
    fireEvent.change(screen.getByLabelText(`${id} 实验不涉及依据`), { target: { value: '此服务未新增实验行为' } })
  }
  fireEvent.click(screen.getByText('S3 · 补充业务要求与覆盖依据'))
  fireEvent.click(screen.getByLabelText('S3 确认不涉及配置'))
  fireEvent.change(screen.getByLabelText('S3 配置不涉及依据'), { target: { value: '此服务只准备实验定义' } })
  fireEvent.click(screen.getByRole('button', { name: '添加实验草稿要求：S3' }))
  for (const [name, value] of [['名称', 'checkout-test'], ['受众', 'test-users'], ['指标（逗号分隔）', 'conversion'], ['理由', '需求要求草稿，等待后续部署与确认']]) fireEvent.change(screen.getByLabelText(`S3 实验 1 ${name}`), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('计划结果未确认')
  fireEvent.click(screen.getByRole('button', { name: '重试本次计划请求' }))
  await screen.findByRole('heading', { name: '计划已整理 · 待人工审阅' })
  expect(posts[1]).toBe(posts[2])
  const update = screen.getByRole('region', { name: '更新配置 feature.timeout' })
  expect(within(update).getByText('S1 的期望')).toBeInTheDocument()
  expect(within(update).getByText('S2 的期望')).toBeInTheDocument()
  expect(screen.getByRole('region', { name: '新增未激活实验草稿 checkout-test' })).toBeInTheDocument()
  expect(screen.getByText('没有写入配置或激活实验。', { exact: false })).toBeInTheDocument()
  const headers = { Authorization: `Bearer ${token}` }
  const first = await (await originalFetch(`${endpoint}/api/v1/changes/change-1/plans/plan-2`, { headers })).json() as ChangePlan
  expect(first.actions).toHaveLength(2)
  expect(first.actions.find(a => a.kind === 'config')?.expectedVersion).toBe('v1')
  fireEvent.click(screen.getByRole('button', { name: '分析并生成新计划' }))
  await waitFor(() => expect(screen.getByLabelText('计划历史')).toHaveValue('plan-3'))
  const next = await (await originalFetch(`${endpoint}/api/v1/changes/change-1/plans/plan-3`, { headers })).json() as ChangePlan
  expect(next.hash).not.toBe(first.hash)
  expect(next.actions.find(a => a.kind === 'config')?.expectedVersion).toBe('v2')
  const reread = await (await originalFetch(`${endpoint}/api/v1/changes/change-1/plans/plan-2`, { headers })).json()
  expect(reread).toEqual(first)
  fireEvent.change(screen.getByLabelText('计划历史'), { target: { value: 'plan-1' } })
  expect(screen.getByRole('heading', { name: '计划存在待补齐或阻断项' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '刷新计划历史' }))
  await screen.findByRole('heading', { name: '计划存在待补齐或阻断项' })
  expect(posts).toHaveLength(4)
  expect(posts.join('')).not.toMatch(/resourceRef|connectorRef|headSha|token|baseUrl/)
}, 30000)
