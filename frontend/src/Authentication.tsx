import { useEffect, useRef, useState, type FormEvent } from 'react'
import { APIContext, authenticatedFetch, type API } from './api'
import TaskWorkspace from './TaskWorkspace'

type Session = { subject: string; api: API }

export default function Authentication() {
  const [token, setToken] = useState('')
  const [session, setSession] = useState<Session | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => controller.current?.abort(), [])

  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    controller.current?.abort()
    const current = new AbortController()
    controller.current = current
    setPending(true)
    setError('')
    try {
      const request = authenticatedFetch(token.trim(), current.signal)
      const response = await request('/api/v1/session')
      if (!response.ok) throw new Error('authentication failed')
      const identity: unknown = await response.json()
      if (current.signal.aborted) return
      if (typeof identity !== 'object' || identity === null ||
        !('subject' in identity) || typeof identity.subject !== 'string' || !identity.subject ||
        !('tenantId' in identity) || typeof identity.tenantId !== 'string' || !identity.tenantId) {
        throw new Error('invalid session')
      }
      setSession({ subject: identity.subject, api: { tenantId: identity.tenantId, fetch: request } })
    } catch {
      current.abort()
      if (controller.current === current) setError('认证失败，请检查本机 API 凭据和服务配置。')
    } finally {
      if (controller.current === current) { setToken(''); setPending(false) }
    }
  }

  function logout() {
    controller.current?.abort()
    controller.current = null
    setSession(null)
    setToken('')
    setError('')
  }

  if (session) return (
    <APIContext.Provider value={session.api}>
      <p>当前身份：{session.subject} · 租户：{session.api.tenantId}</p>
      <button type="button" onClick={logout}>退出身份</button>
      <TaskWorkspace />
    </APIContext.Provider>
  )

  return (
    <form onSubmit={(event) => { void login(event) }}>
      <p id="api-token-help">这里填写本平台生成的登录 token，粘贴完整内容即可，无需 Bearer 前缀。DeepSeek API Key 和 GitLab token 用于服务端连接，不用于此处登录。</p>
      <p>输入本机 API 凭据后开始操作。凭据仅在当前页面内存中使用，刷新后需重新输入。</p>
      <label htmlFor="api-token">API 凭据</label>
      <input id="api-token" type="password" autoComplete="off" aria-describedby="api-token-help" value={token}
        onChange={(event) => setToken(event.target.value)} disabled={pending} required />
      <button type="submit" disabled={pending}>{pending ? '认证中…' : '验证身份'}</button>
      {error && <p role="alert">{error}</p>}
    </form>
  )
}
