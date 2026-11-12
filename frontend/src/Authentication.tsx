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
      <div className="session-bar">
        <span className="breadcrumb">工作台 <span aria-hidden="true">/</span> <strong>代码审阅</strong></span>
        <div className="session-identity">
          <span className="avatar" aria-hidden="true">{session.subject.slice(0, 1).toUpperCase()}</span>
          <span>{session.subject}<small>租户 · {session.api.tenantId}</small></span>
          <button className="button-quiet" type="button" onClick={logout}>退出身份</button>
        </div>
      </div>
      <TaskWorkspace />
    </APIContext.Provider>
  )

  return (
    <section className="login-layout" aria-labelledby="login-title">
      <div className="login-intro">
        <span className="eyebrow">CODE REVIEW, WITH CONTEXT</span>
        <h1 id="login-title">让每一次改动，<br />都经过认真审阅。</h1>
        <p>从固定版本的代码差异出发，找到值得关注的问题，保留每一次审阅的依据。</p>
        <div className="login-flow" aria-label="审阅流程">
          <div><span>01</span><strong>选择代码</strong><p>固定仓库与提交版本</p></div>
          <div><span>02</span><strong>只读审阅</strong><p>分析改动，保留原始代码</p></div>
          <div><span>03</span><strong>人工复核</strong><p>对照差异确认每条建议</p></div>
        </div>
      </div>
      <form className="login-card" onSubmit={(event) => { void login(event) }}>
      <span className="eyebrow">YOUR WORKSPACE</span>
      <h2>进入审阅工作台</h2>
      <p>使用本平台的 API 凭据连接你的工作区。</p>
      <label htmlFor="api-token">API 凭据</label>
      <input id="api-token" type="password" autoComplete="off" aria-describedby="api-token-help" value={token}
        placeholder="粘贴本机 API 凭据" onChange={(event) => setToken(event.target.value)} disabled={pending} required />
      <button className="button-primary" type="submit" disabled={pending}>{pending ? '认证中…' : '验证身份'}</button>
      <p id="api-token-help" className="field-help">粘贴完整 token，无需 Bearer 前缀。这里不使用 DeepSeek API Key 或 GitLab token。</p>
      {error && <p className="error-message" role="alert">{error}</p>}
      <p className="login-note">凭据仅保存在当前页面内存中，刷新后需要重新登录。</p>
      </form>
    </section>
  )
}
