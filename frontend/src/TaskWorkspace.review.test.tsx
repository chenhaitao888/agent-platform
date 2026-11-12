import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { APIContext, authenticatedFetch } from './api'
import TaskWorkspace from './TaskWorkspace'
import WorkspaceDetails from './WorkspaceDetails'
import { deferredResponse, findingsText, installReviewCrypto, reviewExecution, reviewTask, reviewWorkspace } from './test/reviewFixtures'

beforeEach(installReviewCrypto)
afterEach(() => vi.unstubAllGlobals())

it('opens existing results in one action using authenticated reads only', async () => {
  const calls: { url: string; init?: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    calls.push({ url, init })
    if (url.includes('/workspace?')) return Promise.resolve(Response.json(reviewWorkspace))
    if (url.includes('/review?')) return Promise.resolve(Response.json(reviewExecution()))
    if (url.includes('/content?')) return Promise.resolve(new Response(findingsText))
    return Promise.resolve(Response.json({ items: [reviewTask] }))
  }))
  const token = 'readonly-browser-test-token-1234567890'
  const api = { tenantId: reviewTask.tenantId, fetch: authenticatedFetch(token, new AbortController().signal) }
  render(<StrictMode><APIContext.Provider value={api}><TaskWorkspace /></APIContext.Provider></StrictMode>)
  const open = await screen.findByRole('button', { name: '打开审阅工作区：task-1' })
  expect(calls.every(({ url }) => url.startsWith('/api/v1/tasks?'))).toBe(true)
  fireEvent.click(open)
  expect(await screen.findByText('Restore the zero guard')).toBeVisible()
  expect(screen.getByText('divider.py:2')).toBeVisible()
  expect(screen.getByRole('button', { name: '开始新一轮审阅' })).toBeEnabled()
  for (const { init } of calls) {
    expect(init?.method ?? 'GET').toBe('GET')
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
  }
  expect(localStorage.length + sessionStorage.length).toBe(0)
})

it('cancels an automatically loaded artifact on task switch and ignores its late contents', async () => {
  const artifact = deferredResponse()
  let artifactSignal: AbortSignal | undefined
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (url.includes('/workspace?')) return Promise.resolve(Response.json(reviewWorkspace))
    if (url.includes('/review?')) return Promise.resolve(Response.json(reviewExecution()))
    artifactSignal = init?.signal as AbortSignal
    return artifact.promise
  })
  vi.stubGlobal('fetch', fetchMock)
  const api = { tenantId: reviewTask.tenantId, fetch: (url: string, init?: RequestInit) => fetch(url, init) }
  const panel = (id: string) => <APIContext.Provider value={api}><WorkspaceDetails task={{ ...reviewTask, id }} autoReadReview /></APIContext.Provider>
  const { rerender } = render(panel('task-1'))
  fireEvent.click(screen.getByRole('button', { name: '打开审阅工作区：task-1' }))
  await waitFor(() => expect(artifactSignal).toBeDefined())
  rerender(panel('task-2'))
  expect(artifactSignal?.aborted).toBe(true)
  await act(async () => { artifact.resolve(new Response(findingsText)); await artifact.promise })
  expect(screen.queryByText('Restore the zero guard')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '打开审阅工作区：task-2' })).toBeEnabled()
})

it('leaves a task without a review ready for an explicit start', async () => {
  const fetchMock = vi.fn((url: string) => Promise.resolve(url.includes('/workspace?')
    ? Response.json(reviewWorkspace) : Response.json({}, { status: 404 })))
  vi.stubGlobal('fetch', fetchMock)
  const api = { tenantId: reviewTask.tenantId, fetch: (url: string, init?: RequestInit) => fetch(url, init) }
  render(<APIContext.Provider value={api}><WorkspaceDetails task={reviewTask} autoReadReview /></APIContext.Provider>)
  fireEvent.click(screen.getByRole('button', { name: '打开审阅工作区：task-1' }))
  expect(await screen.findByText('尚无审阅记录。')).toBeVisible()
  expect(screen.getByRole('button', { name: '开始只读审阅' })).toBeEnabled()
  expect(fetchMock).toHaveBeenCalledTimes(2)
})
