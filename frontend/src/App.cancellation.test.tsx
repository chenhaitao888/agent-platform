import { act, render, screen } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import App from './App'

afterEach(() => {
  vi.unstubAllGlobals()
})

function deferred<Value>() {
  let resolve: (value: Value) => void = () => undefined
  let reject: (error: Error) => void = () => undefined
  const promise = new Promise<Value>((complete, fail) => {
    resolve = complete
    reject = fail
  })
  return { promise, resolve, reject }
}

function healthResponse(service: string) {
  return Response.json({ status: 'ok', service })
}

describe('App health request cancellation', () => {
  it.each([
    { name: 'cancelled success arrives after current success', previous: 'success', current: 'healthy' },
    { name: 'cancelled HTTP failure arrives after current success', previous: 'http-error', current: 'healthy' },
    { name: 'cancelled network failure arrives after current success', previous: 'network-error', current: 'healthy' },
    { name: 'cancelled AbortError arrives after current success', previous: 'abort', current: 'healthy' },
    { name: 'cancelled success arrives after current HTTP failure', previous: 'success', current: 'unavailable' },
  ])('keeps the current health result when $name', async ({ previous, current }) => {
    const obsolete = deferred<Response>()
    const signals: (AbortSignal | null | undefined)[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        if (input !== '/api/healthz') {
          return Promise.resolve(Response.json({ items: [] }))
        }
        signals.push(init?.signal)
        if (signals.length === 1) return obsolete.promise
        return Promise.resolve(
          current === 'healthy'
            ? healthResponse('current-api-service')
            : Response.json({}, { status: 503 }),
        )
      }),
    )

    function settleObsolete() {
      if (previous === 'network-error') {
        obsolete.reject(new Error('obsolete network failure'))
      } else if (previous === 'abort') {
        obsolete.reject(new DOMException('obsolete request aborted', 'AbortError'))
      } else {
        obsolete.resolve(
          previous === 'success'
            ? healthResponse('obsolete-api-service')
            : Response.json({}, { status: 503 }),
        )
      }
    }

    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    try {
      const expectedStatus = current === 'healthy' ? '运行正常' : '暂时不可用'
      await screen.findByText(expectedStatus)
      expect(signals).toHaveLength(2)
      expect(signals[0]?.aborted).toBe(true)
      expect(signals[1]?.aborted).toBe(false)

      await act(async () => settleObsolete())
      expect(screen.getByRole('status', { name: 'API 状态' })).toHaveTextContent(expectedStatus)
      if (current === 'healthy') {
        expect(screen.getByText('current-api-service')).toBeInTheDocument()
        expect(screen.queryByText(/连接失败：/)).not.toBeInTheDocument()
      } else {
        expect(screen.getByText('连接失败：health check returned HTTP 503')).toBeInTheDocument()
        expect(screen.queryByText('current-api-service')).not.toBeInTheDocument()
      }
      expect(screen.queryByText('obsolete-api-service')).not.toBeInTheDocument()
    } finally {
      await act(async () => settleObsolete())
    }
  })
})
