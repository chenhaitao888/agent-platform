import { useEffect, useRef, useState } from 'react'
import { useAPI } from './api'
import { changeErrors, type ChangeRequest, type ChangeReviewResult, type ChangeReviewRun, type ChangeSnapshot, type ServiceReview } from './change'
import ReviewRecord from './ReviewRecord'
import WorkspaceDetails from './WorkspaceDetails'
import { reviewFailureMessage } from './review'
import type { Task } from './task'

class ChangeReviewError extends Error {}
const labels: Record<string, string> = { NOT_STARTED: '尚未开始', RUNNING: '进行中', SUCCEEDED: '已完成审阅', PARTIAL: '部分完成', FAILED: '失败', CANCELED: '已中止' }
const stages: Record<string, string> = { admission: '准入', preparation: '准备代码', review: '审阅', complete: '完成' }
const failure = (code?: string) => ({ workspace_registration_failed: '仓库准入失败，请检查固定提交是否仍可访问。', workspace_preparation_failed: '工作区准备失败，可重试未完成服务。', task_creation_failed: '无法关联子任务。', task_admission_failed: '子任务准入失败。', request_canceled: '请求已中止，可重试未完成服务。' }[code ?? ''] ?? reviewFailureMessage(code))

export default function ChangeReviews({ change, onVersion, onBusy, blocked = false }: { change: ChangeRequest; onVersion: (version: number) => void; onBusy: (busy: boolean) => void; blocked?: boolean }) {
  const api = useAPI()
  const [runs, setRuns] = useState<ChangeReviewRun[]>([])
  const [selected, setSelected] = useState('')
  const [pending, setPending] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [uncertain, setUncertain] = useState(false)
  const request = useRef<{ snapshotId: string; body: string } | null>(null)
  const lifetime = useRef<AbortController | null>(null)
  const snapshot = change.snapshots.find(s => s.id === (selected || change.currentSnapshotId))
  const history = runs.filter(r => r.snapshotId === snapshot?.id).reverse()
  const active = runs.some(r => r.state === 'RUNNING')
  const latest = history[0]
  const path = `/api/v1/changes/${encodeURIComponent(change.id)}/reviews`

  useEffect(() => {
    const controller = new AbortController(); lifetime.current = controller
    return () => controller.abort()
  }, [api, change.id])

  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      try {
        const [response, currentResponse] = await Promise.all([
          api.fetch(path, { signal: controller.signal }),
          api.fetch(`/api/v1/changes/${encodeURIComponent(change.id)}`, { signal: controller.signal }),
        ])
        if (!response.ok || !currentResponse.ok) throw new Error('unavailable')
        const data = await response.json() as { items: ChangeReviewRun[] }
        const current = await currentResponse.json() as ChangeRequest
        if (!Array.isArray(data.items) || current.id !== change.id || current.tenantId !== api.tenantId || !Number.isSafeInteger(current.version)) throw new Error('invalid response')
        if (controller.signal.aborted) return
        setRuns(data.items); onVersion(current.version)
      } catch { if (!controller.signal.aborted) setError('读取需求审阅状态失败，请刷新审阅状态。') }
      finally { if (!controller.signal.aborted) setLoading(false) }
    }
    void load()
    return () => controller.abort()
  }, [api, path, change.id, refresh, onVersion])

  useEffect(() => {
    if (!pending && !active) return
    const timer = setInterval(() => setRefresh(v => v + 1), 2000)
    return () => clearInterval(timer)
  }, [pending, active])

  async function start() {
    if (!snapshot || pending || active || loading || blocked) return
    const signal = lifetime.current!.signal
    const attempt = request.current ?? { snapshotId: snapshot.id, body: JSON.stringify({ snapshotId: snapshot.id, expectedVersion: change.version, idempotencyKey: crypto.randomUUID() }) }
    request.current = attempt
    setPending(true); onBusy(true); setError('')
    try {
      const response = await api.fetch(path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: attempt.body })
      if (!response.ok) {
        const data = await response.json().catch(() => ({})) as { error?: string }
        throw new ChangeReviewError(changeErrors[data.error ?? ''] ?? `审阅请求失败（HTTP ${response.status}）`)
      }
      const result = await response.json() as ChangeReviewResult
      if (result.changeId !== change.id || result.review?.snapshotId !== attempt.snapshotId || !Array.isArray(result.review.services) || !Number.isSafeInteger(result.changeVersion)) throw new Error('invalid response')
      if (signal.aborted) return
      setRuns(previous => [...previous.filter(r => r.id !== result.review.id), result.review])
      onVersion(result.changeVersion); request.current = null; setUncertain(false)
    } catch (err) {
      if (signal.aborted) return
      if (err instanceof ChangeReviewError) { request.current = null; setUncertain(false); setError(err.message) }
      else { setUncertain(true); setError('审阅结果未确认；重试本次请求将使用同一标识，不会重复已完成工作。') }
    } finally { if (!signal.aborted) { setPending(false); onBusy(false); setRefresh(v => v + 1) } }
  }

  return <section className="change-reviews" aria-label="需求审阅">
    <h3>多服务审阅</h3>
    <p>逐服务审阅固定版本；失败后仅重试未完成服务。审阅完成后，请逐项判断问题是否需要处理。</p>
    <label>审阅快照<select value={snapshot?.id ?? ''} disabled={pending || uncertain} onChange={event => { setSelected(event.target.value); setError('') }}>
      {change.snapshots.filter(s => s.state === 'READY').map(s => <option key={s.id} value={s.id}>快照 {s.revision}{s.id === change.currentSnapshotId ? '（当前）' : '（历史）'}</option>)}
    </select></label>
    {snapshot && snapshot.id !== change.currentSnapshotId && <p role="note">当前查看的是历史快照 {snapshot.revision}，结论不适用于新快照。</p>}
    <div className="button-row">
      <button type="button" className="button-primary" disabled={loading || pending || active || blocked || !snapshot || (!uncertain && latest?.state === 'SUCCEEDED')} onClick={() => void start()}>
        {pending || active ? '审阅进行中…' : uncertain ? '重试本次审阅请求' : latest && latest.state !== 'SUCCEEDED' ? '重试未完成服务' : '开始多服务审阅'}
      </button>
      <button type="button" onClick={() => { setError(''); setRefresh(v => v + 1) }}>刷新审阅状态</button>
    </div>
    {loading && <p role="status">正在读取审阅状态…</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {!loading && !latest && <p>该快照尚未审阅。查看快照或刷新状态不会调用模型。</p>}
    {snapshot && history.map(run => <ReviewRunCard key={run.id} run={run} snapshot={snapshot} change={change} />)}
  </section>
}

function ReviewRunCard({ run, snapshot, change }: { run: ChangeReviewRun; snapshot: ChangeSnapshot; change: ChangeRequest }) {
  const [opened, setOpened] = useState('')
  if (run.snapshotHash !== snapshot.inputHash) return <p role="alert">审阅记录与快照不一致，请重新读取需求。</p>
  return <article className="snapshot-card" aria-label={`快照 ${snapshot.revision} 审阅 ${run.id}`}>
    <h4>快照 {snapshot.revision} · {labels[run.state]}</h4>
    <p>{run.services.filter(s => s.state === 'SUCCEEDED').length} / {run.services.length} 个服务完成 · {run.startedAt}</p>
    <ul>{run.services.map(item => <li key={item.serviceId}>
      <strong>{item.serviceId} · {labels[item.state]}</strong>
      <p>{stages[item.stage]}{item.reused ? ' · 沿用该快照已完成的结果' : ''}</p>
      {item.errorCode && <p className="error-message">{failure(item.errorCode)}</p>}
      {item.taskId && <button type="button" onClick={() => setOpened(opened === item.serviceId ? '' : item.serviceId)}>{opened === item.serviceId ? '收起' : '查看'} {item.serviceId} 结果</button>}
      {opened === item.serviceId && <ServiceEvidence key={`${run.id}:${item.serviceId}:${item.executionId ?? ''}`} change={change} snapshot={snapshot} item={item} />}
    </li>)}</ul>
  </article>
}

function ServiceEvidence({ change, snapshot, item }: { change: ChangeRequest; snapshot: ChangeSnapshot; item: ServiceReview }) {
  const api = useAPI()
  const [task, setTask] = useState<Task | null>(null)
  const [error, setError] = useState('')
  const reference = snapshot.services.find(s => s.serviceId === item.serviceId)?.repository
  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      try {
        const response = await api.fetch(`/api/v1/tasks/${encodeURIComponent(item.taskId!)}?${new URLSearchParams({ tenantId: api.tenantId })}`, { signal: controller.signal })
        if (!response.ok) throw new Error('unavailable')
        const child = await response.json() as Task
        if (!reference || child.tenantId !== api.tenantId || change.tenantId !== api.tenantId || child.id !== item.taskId || child.change?.changeId !== change.id || child.change.snapshotId !== snapshot.id || child.change.serviceId !== item.serviceId ||
          Object.entries(reference).some(([key, value]) => child.repository[key as keyof typeof reference] !== value)) throw new Error('mismatched evidence')
        if (!controller.signal.aborted) setTask(child)
      } catch { if (!controller.signal.aborted) setError('无法读取与该服务快照一致的任务记录，请重新打开结果。') }
    }
    void load(); return () => controller.abort()
  }, [api, change.id, change.tenantId, snapshot.id, item.taskId, item.serviceId, reference])
  return <div className="service-evidence">
    {error && <p role="alert" className="error-message">{error}</p>}
    {!task && !error && <p role="status">正在读取服务结果…</p>}
    {task && <>
      <WorkspaceDetails task={task} />
      {item.executionId && item.workspaceId && item.workspaceVersion && <ReviewRecord task={task} reference={{ executionId: item.executionId, workspaceId: item.workspaceId, workspaceVersion: item.workspaceVersion, artifactId: item.artifactId }} autoReadFindings />}
    </>}
  </div>
}
