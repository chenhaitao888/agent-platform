import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import type { ChangeReviewRun } from './change'

const { env } = await vi.importActual<{ env: Record<string, string | undefined> }>('node:process')
const endpoint = env.AGENT_PLATFORM_CHANGE_REVIEW_URL
afterEach(() => vi.unstubAllGlobals())
it.skipIf(!endpoint)('reviews three services, recovers response loss, retries a failure and isolates new snapshot evidence over HTTP', async () => {
  const originalFetch=globalThis.fetch
  const token='api-test-token-with-at-least-32-characters'
  const posts: string[]=[]
  const requests: {path: string;method?: string}[]=[]
  vi.stubGlobal('fetch', async (path: string,init?: RequestInit) => {
    requests.push({path,method:init?.method})
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    const response=await originalFetch(`${endpoint}${path}`,init)
    if (path.endsWith('/reviews') && init?.method==='POST') {
      posts.push(init.body as string)
      if (posts.length===1 && response.ok) { await response.arrayBuffer(); throw new TypeError('controlled response loss') }
    }
    return response
  })
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'),{target:{value:token}})
  fireEvent.click(screen.getByRole('button',{name:'验证身份'}))
  await screen.findByText('暂无需求')
  fireEvent.change(screen.getByLabelText('需求名称或编号'),{target:{value:'跨服务审阅需求'}})
  for (const id of ['S1','S2','S3']) {
    fireEvent.click(screen.getByLabelText(`选择 ${id}`))
    fireEvent.change(screen.getByLabelText(`${id} 源分支`),{target:{value:'feature'}})
  }
  fireEvent.click(screen.getByRole('button',{name:'创建需求'}))
  fireEvent.click(await screen.findByRole('button',{name:'固定代码快照'}))
  const start=await screen.findByRole('button',{name:'开始多服务审阅'})
  await waitFor(() => expect(start).toBeEnabled())
  expect(posts).toHaveLength(0)
  fireEvent.click(start)
  expect(await screen.findByRole('alert',{}, {timeout:15000})).toHaveTextContent('审阅结果未确认')
  fireEvent.click(screen.getByRole('button',{name:'重试本次审阅请求'}))
  const retry=await screen.findByRole('button',{name:'重试未完成服务'})
  expect(posts[0]).toBe(posts[1])
  expect(screen.getByRole('heading',{name:'快照 1 · 部分完成'})).toBeInTheDocument()
  fireEvent.click(retry)
  await screen.findByRole('heading',{name:'快照 1 · 已完成审阅'}, {timeout:15000})
  const successful=screen.getByRole('article',{name:'快照 1 审阅 change-review-2'})
  expect(within(successful).getAllByText(/沿用该快照已完成的结果/)).toHaveLength(2)
  fireEvent.click(within(successful).getByRole('button',{name:'查看 S2 结果'}))
  await within(successful).findByText('Controlled service finding')
  fireEvent.click(within(successful).getByRole('button',{name:'打开审阅工作区：task-2'}))
  fireEvent.click(await within(successful).findByRole('button',{name:'查看固定版本差异'}))
  await within(successful).findByText(/\+feature-only change/)
  expect(within(successful).queryByRole('button',{name:'开始审阅'})).not.toBeInTheDocument()
  const headers={Authorization:`Bearer ${token}`}
  const firstRuns=(await (await originalFetch(`${endpoint}/api/v1/changes/change-1/reviews`,{headers})).json()).items as ChangeReviewRun[]
  const failed=firstRuns[0].services[0]
  expect(firstRuns.map(r=>r.state)).toEqual(['PARTIAL','SUCCEEDED'])
  expect(firstRuns[1].services[0].taskId).toBe(failed.taskId)
  expect(firstRuns[1].services[0].executionId).not.toBe(failed.executionId)
  expect(firstRuns[1].services[1].executionId).toBe(firstRuns[0].services[1].executionId)
  const oldExecution=await originalFetch(`${endpoint}/api/v1/tasks/${failed.taskId}/reviews/${failed.executionId}?tenantId=test`,{headers})
  expect((await oldExecution.json()).state).toBe('FAILED')
  fireEvent.click(screen.getByRole('button',{name:'创建新快照'}))
  await screen.findByRole('heading',{name:'快照 2'})
  await screen.findByText('该快照尚未审阅。查看快照或刷新状态不会调用模型。')
  expect(screen.queryByText('快照 1 · 已完成审阅')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button',{name:'开始多服务审阅'}))
  await screen.findByRole('heading',{name:'快照 2 · 已完成审阅'}, {timeout:15000})
  expect(posts).toHaveLength(4)
  fireEvent.change(screen.getByLabelText('审阅快照'),{target:{value:'snapshot-1'}})
  expect(screen.getByRole('note')).toHaveTextContent('历史快照 1')
  fireEvent.click(screen.getByRole('button',{name:'刷新审阅状态'}))
  await screen.findByRole('heading',{name:'快照 1 · 部分完成'})
  expect(posts).toHaveLength(4)
  expect(requests.filter(r=>r.method==='POST' && r.path.startsWith('/api/v1/tasks/'))).toHaveLength(0)
},30000)
