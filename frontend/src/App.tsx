import { useEffect, useState } from 'react'
import TaskWorkspace from './TaskWorkspace'

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
    <main className="page-shell">
      <section className="status-card" aria-labelledby="page-title">
        <p className="eyebrow">ENTERPRISE AGENT CONTROL PLANE</p>
        <h1 id="page-title">Agent Platform</h1>
        <p className="intro">
          {pageState.kind === 'loading' && '正在检查 API 连接…'}
          {pageState.kind === 'healthy' &&
            'API 连接正常，可以在下方创建 Task 并查看最近记录。'}
          {pageState.kind === 'unavailable' &&
            'API 健康检查未通过，请查看下方错误信息。'}
        </p>

        <div className={`status-panel status-panel--${pageState.kind}`}>
          <span className="status-dot" aria-hidden="true" />
          <div>
            <p className="status-label">API 状态</p>
            <p className="status-value" role="status" aria-label="API 状态">
              {pageState.kind === 'loading' && '检查中…'}
              {pageState.kind === 'healthy' && '运行正常'}
              {pageState.kind === 'unavailable' && '暂时不可用'}
            </p>
          </div>
        </div>

        <dl className="details">
          <div>
            <dt>服务</dt>
            <dd>{isHealthy ? pageState.health.service : 'agent-platform-api'}</dd>
          </div>
          <div>
            <dt>健康接口</dt>
            <dd>/healthz</dd>
          </div>
          <div>
            <dt>本步范围</dt>
            <dd>Task 创建与列表</dd>
          </div>
        </dl>

        {pageState.kind === 'unavailable' && (
          <p className="error-message">连接失败：{pageState.message}</p>
        )}

        <TaskWorkspace />
      </section>
    </main>
  )
}
