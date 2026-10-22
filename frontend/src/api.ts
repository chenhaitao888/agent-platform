import { createContext, useContext } from 'react'

export type API = {
  tenantId: string
  fetch: (path: string, init?: RequestInit) => Promise<Response>
}

// Isolated business components can render without a session; their requests
// remain unauthenticated and are rejected by the API. App supplies the session.
export const APIContext = createContext<API>({
  tenantId: 'tenant-local',
  fetch: (path, init) => fetch(path, init),
})

export function useAPI() { return useContext(APIContext) }

export function authenticatedFetch(token: string, lifetime: AbortSignal): API['fetch'] {
  return (path, init) => {
    if (!path.startsWith('/api/v1/') || path.includes('\\')) {
      return Promise.reject(new Error('请求地址不受支持'))
    }
    const headers = new Headers(init?.headers)
    headers.set('Authorization', `Bearer ${token}`)
    return fetch(path, {
      ...init, headers, credentials: 'omit', redirect: 'error', cache: 'no-store',
      signal: init?.signal ? AbortSignal.any([lifetime, init.signal]) : lifetime,
    })
  }
}
