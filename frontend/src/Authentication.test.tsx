import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import Authentication from './Authentication'
import ArtifactContent from './ArtifactContent'
import { APIContext, authenticatedFetch } from './api'

const token = 'browser-test-token-only-12345678901234567890'
afterEach(() => vi.unstubAllGlobals())

it('derives the tenant from authentication, authenticates business calls and clears the session on logout', async () => {
  const calls: { path: string; init?: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn((path: string, init?: RequestInit) => {
    calls.push({ path, init })
    return Promise.resolve(new Response(JSON.stringify(path === '/api/v1/session'
      ? { subject: 'alice', tenantId: 'tenant-from-server' } : { items: [] }), { status: 200 }))
  }))
  render(<Authentication />)
  expect(calls).toHaveLength(0)
  expect(screen.queryByRole('heading', { name: '最近 Task' })).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  await screen.findByRole('heading', { name: '最近 Task' })
  await waitFor(() => expect(calls).toHaveLength(2))
  expect(calls[1].path).toBe('/api/v1/tasks?tenantId=tenant-from-server')
  for (const call of calls) {
    expect(new Headers(call.init?.headers).get('Authorization')).toBe(`Bearer ${token}`)
    expect(call.init?.redirect).toBe('error')
    expect(call.path).not.toContain(token)
  }
  expect(localStorage.length).toBe(0)
  expect(sessionStorage.length).toBe(0)
  fireEvent.click(screen.getByRole('button', { name: '退出身份' }))
  expect(screen.queryByRole('heading', { name: '最近 Task' })).not.toBeInTheDocument()
  expect(screen.getByLabelText('API 凭据')).toHaveValue('')
  expect(calls.every((call) => call.init?.signal?.aborted)).toBe(true)
})

it('keeps failed authentication closed and does not render upstream credential echoes', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(token, { status: 401 }))
  vi.stubGlobal('fetch', fetchMock)
  render(<Authentication />)
  fireEvent.change(screen.getByLabelText('API 凭据'), { target: { value: token } })
  fireEvent.click(screen.getByRole('button', { name: '验证身份' }))
  expect(await screen.findByRole('alert')).not.toHaveTextContent(token)
  expect(fetchMock).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('heading', { name: '最近 Task' })).not.toBeInTheDocument()
})

it('reads artifact content with Authorization instead of navigating to an unauthenticated URL', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response('controlled diff', { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  const request = authenticatedFetch(token, new AbortController().signal)
  render(<APIContext.Provider value={{ tenantId: 'tenant-a', fetch: request }}>
    <ArtifactContent id="artifact-1" tenantId="tenant-a" />
  </APIContext.Provider>)
  fireEvent.click(screen.getByRole('button', { name: '读取 Artifact 内容' }))
  expect(await screen.findByText('controlled diff')).toBeInTheDocument()
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/artifacts/artifact-1/content?tenantId=tenant-a')
  expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Authorization')).toBe(`Bearer ${token}`)
  await expect(request('https://example.test/api/v1/tasks')).rejects.toThrow()
  expect(fetchMock).toHaveBeenCalledTimes(1)
})
