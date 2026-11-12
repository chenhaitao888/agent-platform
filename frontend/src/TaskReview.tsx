import { useCallback, useEffect, useRef, useState } from 'react'
import { useAPI } from './api'
import ReviewFindings from './ReviewFindings'
import { parseReviewExecution, reviewFailureMessage, reviewHTTPError, ReviewResponseError, type ReviewExecution } from './review'
import type { Task } from './task'
import type { Workspace } from './workspace'

type Props = { task: Task; workspace: Workspace; autoRead?: boolean }
type Status = { kind: 'unknown' | 'absent' } | { kind: 'loaded'; execution: ReviewExecution }
type Attempt = { key: string; previousExecutionId: string | null }

export default function TaskReview({ task, workspace, autoRead = false }: Props) {
  const api = useAPI()
  if (task.type !== 'PR_REVIEW' || task.status !== 'QUEUED' || workspace.state !== 'READY') return null
  if (task.tenantId !== api.tenantId || workspace.tenantId !== api.tenantId || workspace.taskId !== task.id ||
    workspace.baseSha !== task.repository.baseSha || workspace.headSha !== task.repository.headSha) {
    return <p role="alert">当前身份、Task 与 Workspace 不一致，请重新加载。</p>
  }
  // Scope changes unmount the old request, findings and in-memory attempt key.
  return <ReviewPanel key={JSON.stringify([api.tenantId, task.id, workspace.id, workspace.version, workspace.baseSha, workspace.headSha])} task={task} workspace={workspace} autoRead={autoRead} />
}

function ReviewPanel({ task, workspace, autoRead }: Props) {
  const api = useAPI()
  const [status, setStatus] = useState<Status>({ kind: 'unknown' })
  const [attempt, setAttempt] = useState<Attempt | null>(null)
  const [pending, setPending] = useState<'status' | 'execute' | null>(null)
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)
  const lifetime = useRef<AbortController | null>(null)
  const busy = useRef(false)
  useEffect(() => {
    const current = new AbortController()
    lifetime.current = current
    busy.current = false
    setStatus({ kind: 'unknown' }); setAttempt(null); setPending(null); setError(''); setBlocked(false)
    return () => current.abort()
  }, [api])

  const execution = status.kind === 'loaded' ? status.execution : null
  const running = execution?.state === 'RUNNING'
  const canStart = status.kind !== 'unknown' && !running && !attempt && !blocked
  const address = `/api/v1/tasks/${encodeURIComponent(task.id)}/review`

  const loadStatus = useCallback(async () => {
    const signal = lifetime.current?.signal
    if (!signal || signal.aborted || busy.current) return
    busy.current = true
    setPending('status'); setError(''); setStatus({ kind: 'unknown' })
    try {
      const response = await api.fetch(`${address}?${new URLSearchParams({ tenantId: api.tenantId })}`, { signal })
      if (signal.aborted) return
      if (response.status === 404) { setStatus({ kind: 'absent' }); return }
      if (!response.ok) {
        const failure = await reviewHTTPError(response)
        if (!signal.aborted) { setError(failure.message); if (failure.blocked) setBlocked(true) }
        return
      }
      const current = parseReviewExecution(await response.json(), workspace)
      if (signal.aborted) return
      setStatus({ kind: 'loaded', execution: current })
      // A stale terminal snapshot from before the uncertain POST cannot resolve
      // that attempt. Only a different terminal execution permits a new key.
      if (current.state !== 'RUNNING') setAttempt((previous) => previous?.previousExecutionId === current.id ? previous : null)
    } catch (error) {
      if (!signal.aborted) setError(error instanceof ReviewResponseError ? error.message : '无法读取审阅状态，请检查连接后重试。')
    } finally {
      if (!signal.aborted) { busy.current = false; setPending(null) }
    }
  }, [api, address, workspace])

  useEffect(() => { if (autoRead) void loadStatus() }, [autoRead, loadStatus])

  async function execute() {
    const signal = lifetime.current?.signal
    if (!signal || signal.aborted || busy.current || blocked || running || (!attempt && !canStart)) return
    busy.current = true
    const current = attempt ?? { key: `review:v1:${crypto.randomUUID()}`, previousExecutionId: execution?.id ?? null }
    setAttempt(current); setPending('execute'); setError(''); setStatus({ kind: 'unknown' })
    try {
      const response = await api.fetch(address, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ requestId: `req_${crypto.randomUUID()}`, tenantId: api.tenantId,
          idempotencyKey: current.key, expectedWorkspaceVersion: workspace.version }) })
      if (signal.aborted) return
      if (!response.ok) {
        const failure = await reviewHTTPError(response)
        if (!signal.aborted) { setError(failure.message); if (failure.blocked) setBlocked(true) }
        return
      }
      const result = parseReviewExecution(await response.json(), workspace)
      if (result.state !== 'SUCCEEDED') throw new ReviewResponseError('审阅响应未确认成功，请刷新状态。')
      if (signal.aborted) return
      setStatus({ kind: 'loaded', execution: result }); setAttempt(null)
    } catch (error) {
      if (!signal.aborted) setError(error instanceof ReviewResponseError ? error.message : '请求结果未确认，请刷新审阅状态或重试本次请求。')
    } finally {
      if (!signal.aborted) { busy.current = false; setPending(null) }
    }
  }

  const stateLabels = { RUNNING: '审阅进行中', SUCCEEDED: '审阅成功', FAILED: '审阅失败', CANCELED: '审阅已取消' }
  return <section className="task-review" aria-label={`${task.id} 的只读审阅`}>
    <div className="pane-heading"><h3>审阅结果</h3><span className="readonly-label">只读审阅</span></div>
    <p className="review-help">{autoRead ? '已有结果会自动读取。模型建议需要由你最终确认。' : '先查看已有状态，再选择是否开始审阅。'}</p>
    {pending === 'status' && <p className="loading-message" role="status">正在读取已有审阅…</p>}
    {pending === 'execute' && <p className="loading-message" role="status">审阅请求处理中…</p>}
    {status.kind === 'absent' && <div className="review-empty"><p>尚无审阅记录。</p><small>代码已就绪，可以开始第一次只读审阅。</small></div>}
    {error && <p className="error-message" role="alert">{error}</p>}
    {execution && <>
      <p className={`review-state review-state--${execution.state.toLowerCase()}`} role="status">{stateLabels[execution.state]}<small>{execution.id}</small></p>
      {(execution.state === 'FAILED' || execution.state === 'CANCELED') && <p>{reviewFailureMessage(execution.errorCode)}</p>}
      {execution.state === 'SUCCEEDED' && <ReviewFindings key={execution.id} execution={execution} autoRead={autoRead} />}
      <details className="technical-details"><summary>审阅版本信息</summary><dl className="metadata">
        <div><dt>审阅 Base SHA</dt><dd>{execution.baseSha}</dd></div>
        <div><dt>审阅 Head SHA</dt><dd>{execution.headSha}</dd></div>
      </dl></details>
    </>}
    <div className="review-actions">
      <button type="button" aria-label={`查看 ${task.id} 的审阅状态`} disabled={pending !== null} onClick={() => { void loadStatus() }}>
        {pending === 'status' ? '正在读取状态…' : '刷新审阅状态'}
      </button>
      {canStart && <button className={execution ? '' : 'button-primary'} type="button" disabled={pending !== null} onClick={() => { void execute() }}>
        {execution ? '开始新一轮审阅' : '开始只读审阅'}
      </button>}
      {attempt && <button type="button" disabled={pending !== null || running || blocked} onClick={() => { void execute() }}>重试本次请求</button>}
    </div>
    {canStart && <p className="field-help">开始{execution ? '新一轮' : ''}审阅会调用模型，不会修改仓库代码。</p>}
  </section>
}
