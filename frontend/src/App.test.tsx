import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import App from './App'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('App', () => {
  it('shows the real backend service when the health check succeeds', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const body =
        input === '/api/healthz'
          ? { status: 'ok', service: 'agent-platform-api' }
          : { items: [] }
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    })
    vi.stubGlobal(
      'fetch',
      fetchMock,
    )

    render(<App />)

    expect(screen.getByRole('status', { name: 'API 状态' })).toHaveTextContent(
      '检查中',
    )
    expect(await screen.findByText('运行正常')).toBeInTheDocument()
    expect(screen.getByText('agent-platform-api')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '创建 Task' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: '最近 Task' }),
    ).toBeInTheDocument()
  })

  it('shows an unavailable state when the health check fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        if (input === '/api/healthz') {
          return Promise.reject(new Error('network down'))
        }
        return Promise.resolve(
          new Response(JSON.stringify({ items: [] }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        )
      }),
    )

    render(<App />)

    expect(await screen.findByText('暂时不可用')).toBeInTheDocument()
    expect(screen.getByText('连接失败：network down')).toBeInTheDocument()
  })
})
