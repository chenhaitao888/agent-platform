import { useEffect, useState } from 'react'
import { useAPI } from './api'
import ReviewFindings from './ReviewFindings'
import { parseReviewExecution, reviewFailureMessage, reviewHTTPError, ReviewResponseError, type ReviewExecution } from './review'
import type { Task } from './task'

export type ReviewReference = {
  executionId: string
  workspaceId: string
  workspaceVersion: number
  artifactId?: string
}

export default function ReviewRecord({ task, reference, autoReadFindings = false }: { task: Task; reference: ReviewReference; autoReadFindings?: boolean }) {
  const api = useAPI()
  const [execution, setExecution] = useState<ReviewExecution | null>(null)
  const [pending, setPending] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const { executionId, workspaceId, workspaceVersion, artifactId } = reference
  const { id: taskId, tenantId, repository: { baseSha, headSha } } = task

  useEffect(() => {
    const controller = new AbortController()
    const { signal } = controller
    setExecution(null); setError(''); setPending(true)
    async function read() {
      try {
        if (tenantId !== api.tenantId || typeof executionId !== 'string' || !executionId ||
          typeof workspaceId !== 'string' || !workspaceId || !Number.isSafeInteger(workspaceVersion) || workspaceVersion <= 0) {
          throw new ReviewResponseError('事件引用与当前身份不一致或格式无效，请重新加载事件。')
        }
        const query = new URLSearchParams({ tenantId: api.tenantId })
        const response = await api.fetch(`/api/v1/tasks/${encodeURIComponent(taskId)}/reviews/${encodeURIComponent(executionId)}?${query}`, { signal })
        if (signal.aborted) return
        if (!response.ok) throw new ReviewResponseError((await reviewHTTPError(response)).message)
        const result = parseReviewExecution(await response.json(), { id: workspaceId, tenantId, taskId,
          version: workspaceVersion, baseSha, headSha })
        if (result.id !== executionId || (artifactId !== undefined && result.artifact?.id !== artifactId)) {
          throw new ReviewResponseError('审阅记录与所选事件不一致，请重新加载事件。')
        }
        if (!signal.aborted) setExecution(result)
      } catch (error) {
        if (!signal.aborted) setError(error instanceof ReviewResponseError ? error.message : '无法读取审阅记录，请检查连接后重试。')
      } finally {
        if (!signal.aborted) setPending(false)
      }
    }
    void read()
    return () => controller.abort()
  }, [api, taskId, tenantId, baseSha, headSha, executionId, workspaceId, workspaceVersion, artifactId, refresh])

  const labels = { RUNNING: '审阅进行中', SUCCEEDED: '本次审阅成功', FAILED: '本次审阅失败', CANCELED: '本次审阅已取消' }
  return <section className="task-review history-review" aria-label={`执行 ${executionId} 的记录`}>
    <h3>审阅记录：{executionId}</h3>
    <p>显示所选事件对应的执行，刷新记录只读取状态。</p>
    <button type="button" disabled={pending} onClick={() => setRefresh((value) => value + 1)}>刷新执行记录</button>
    {pending && <p role="status">正在读取审阅记录…</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {execution && <>
      <p className={`review-state review-state--${execution.state.toLowerCase()}`} role="status">{labels[execution.state]}</p>
      <details className="technical-details"><summary>执行时间与版本</summary><dl className="metadata">
        <div><dt>Workspace 版本</dt><dd>{execution.workspaceVersion}</dd></div>
        <div><dt>开始时间</dt><dd>{execution.startedAt}</dd></div>
        {execution.completedAt && <div><dt>结束时间</dt><dd>{execution.completedAt}</dd></div>}
        <div><dt>审阅 Base SHA</dt><dd>{execution.baseSha}</dd></div>
        <div><dt>审阅 Head SHA</dt><dd>{execution.headSha}</dd></div>
      </dl></details>
      {(execution.state === 'FAILED' || execution.state === 'CANCELED') && <p>{reviewFailureMessage(execution.errorCode)}</p>}
      {execution.state === 'SUCCEEDED' && <ReviewFindings key={execution.id} execution={execution} autoRead={autoReadFindings} />}
    </>}
  </section>
}
