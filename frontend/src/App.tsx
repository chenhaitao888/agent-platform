import { useEffect, useState } from 'react'
import Authentication from './Authentication'

type HealthResponse = {
  status: 'ok'
  service: string
}

type PageState =
  | { kind: 'loading' }
  | { kind: 'healthy'; health: HealthResponse }
  | { kind: 'unavailable'; message: string }

export default function App() {
  const [pageState, setPageState] = useState<PageState>({ kind: 'loading' })

  useEffect(() => {
    const controller = new AbortController()

    async function loadHealth() {
      try {
        const response = await fetch('/api/healthz', {
          signal: controller.signal,
        })
        if (controller.signal.aborted) return

        if (!response.ok) {
          throw new Error(`health check returned HTTP ${response.status}`)
        }

        const health = (await response.json()) as HealthResponse
        if (controller.signal.aborted) return
        setPageState({ kind: 'healthy', health })
      } catch (error) {
        if (
          controller.signal.aborted ||
          (error instanceof DOMException && error.name === 'AbortError')
        ) {
          return
        }

        const message = error instanceof Error ? error.message : 'unknown error'
        setPageState({ kind: 'unavailable', message })
      }
    }

    void loadHealth()

    return () => controller.abort()
  }, [])

  const isHealthy = pageState.kind === 'healthy'

  return (
    <div className="app-shell">
      <header className="app-header">
        <a className="brand" href="#main-content" aria-label="Agent Platform 审阅工作台">
          <span className="brand-mark" aria-hidden="true">a<span>·</span></span>
          <span>Agent Platform<small>代码审阅工作台</small></span>
        </a>
        <div className="header-tools">
          <span className="pilot-badge">本机试点</span>
          <div className={`status-panel status-panel--${pageState.kind}`}>
            <span className="status-dot" aria-hidden="true" />
            <span role="status" aria-label="API 状态">
              {pageState.kind === 'loading' && '检查中…'}
              {pageState.kind === 'healthy' && '运行正常'}
              {pageState.kind === 'unavailable' && '暂时不可用'}
            </span>
          </div>
          <details className="connection-details">
            <summary>连接详情</summary>
            <dl className="metadata">
              <div><dt>服务</dt><dd>{isHealthy ? pageState.health.service : 'agent-platform-api'}</dd></div>
              <div><dt>健康接口</dt><dd>/healthz</dd></div>
            </dl>
          </details>
        </div>
      </header>
      <main className="page-shell" id="main-content">
        {pageState.kind === 'unavailable' && (
          <p className="error-message" role="alert">连接失败：{pageState.message}</p>
        )}
        <Authentication />
      </main>
    </div>
  )
}
