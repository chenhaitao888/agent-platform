import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useAPI } from './api'
import type { DeliveryBundle } from './delivery'
import { valueText, type PlannedValue } from './planning'

type Execution = { id: string; state: string; evidenceHash?: string }
type Item = { operationId: string; key: string; disposition: string; code: string; currentValue?: PlannedValue; restoreValue?: PlannedValue }
type Report = { schemaVersion: string; id: string; tenantId: string; sourceBundleId: string; sourceExecutionId: string; state: string; reason: string; errorCode?: string; items: Item[]; derivedBundleId?: string }
const labels: Record<string, string> = { READY_FOR_REVIEW: '可审阅补偿动作', MANUAL_ONLY: '仅需人工处置', NO_ACTION_NEEDED: '无需补偿', BLOCKED: '暂不能生成可执行补偿', RESTORE_CONFIG: '可准备配置恢复', MANUAL: '人工处置', NO_EFFECT: '无需恢复', UNRESOLVED: '先核对执行结果' }
class CompensationError extends Error {}
const reasons: Record<string, string> = { compensation_revalidation_unavailable: '暂时无法重验代码或资源，请恢复连接后重新准备。', restore_previous_value: '恢复本次写入前的配置值，需另行批准。', not_dispatched: '本项没有发出写请求。', rejected: '平台已明确拒绝本项。', no_change: '原动作没有改变配置。', outcome_unknown: '尚未取得可靠回执，请先只读核对。', source_not_settled: '原执行仍有未确认结果，请先核对后重新准备计划。', deletion_unsupported: '不支持自动删除新增配置或实验草稿，请人工处理。', experiment_restore_unsupported: '实验定义恢复需人工处理。', source_not_verified: '原写入未通过核验，不能据此自动恢复。', resource_unavailable: '暂时无法读取当前资源，请恢复连接后生成新计划。', resource_changed: '资源存在后续变化，不能覆盖，请人工核对。', restore_constraints_changed: '原值不再满足当前类型、范围或密钥引用约束。', compensation_input_changed: '代码或资源条件已变化，请核对后生成新计划。' }

export default function CompensationPlans({ bundle, runs, canPrepare, onOpen }: { bundle: DeliveryBundle; runs: Execution[]; canPrepare: boolean; onOpen?: (id: string) => void }) {
  const api = useAPI()
  const [reports, setReports] = useState<Report[]>([])
  const [reason, setReason] = useState('')
  const [selected, setSelected] = useState('')
  const [pending, setPending] = useState(false)
  const [unknown, setUnknown] = useState(false)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const busy = useRef(false)
  const attempt = useRef<{ path: string; body: string } | null>(null)
  const life = useRef<AbortController | null>(null)
  const root = `/api/v1/deliveries/${encodeURIComponent(bundle.id)}`
  const settled = runs.filter(r => !['QUEUED', 'RUNNING'].includes(r.state) && r.evidenceHash)
  const run = settled.find(r => r.id === selected) ?? settled[0]
  useEffect(() => { const c = new AbortController(); life.current = c; return () => c.abort() }, [])
  useEffect(() => {
    const c = new AbortController()
    async function load() {
      try {
        const response = await api.fetch(`${root}/compensations`, { signal: c.signal })
        if (!response.ok) throw new Error('unavailable')
        const data = await response.json() as { items: Report[] }
        if (!Array.isArray(data.items) || data.items.some(p => p.tenantId !== api.tenantId || p.sourceBundleId !== bundle.id || p.schemaVersion !== 'compensation-plan.v1' || !Array.isArray(p.items))) throw new Error('mismatched report')
        if (!c.signal.aborted) setReports(data.items)
      } catch { if (!c.signal.aborted) { setReports([]); setError('无法读取当前身份的处置计划。') } }
    }
    void load(); return () => c.abort()
  }, [api, root, bundle.id, refresh])
  async function prepare(event?: FormEvent) {
    event?.preventDefault()
    if (busy.current || !life.current || life.current.signal.aborted || (!attempt.current && !run)) return
    const signal = life.current.signal
    const request = attempt.current ?? { path: `${root}/write-executions/${encodeURIComponent(run!.id)}/compensations`, body: JSON.stringify({ idempotencyKey: crypto.randomUUID(), expectedExecutionHash: run!.evidenceHash, reason }) }
    attempt.current = request; busy.current = true; setPending(true); setError('')
    try {
      const response = await api.fetch(request.path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: request.body })
      if (signal.aborted) return
      if (!response.ok) {
        if (response.status < 500) { attempt.current = null; setUnknown(false) }
        throw new CompensationError(response.status === 403 ? '当前身份无权准备补偿计划。' : response.status === 409 ? '执行记录或准备条件已变化，请刷新写入记录后重试。' : '准备结果未确认，请保留本次请求重试。')
      }
      const report = await response.json() as Report
      if (report.tenantId !== api.tenantId || report.sourceBundleId !== bundle.id || report.schemaVersion !== 'compensation-plan.v1') throw new CompensationError('准备结果与当前交付包不一致。')
      if (signal.aborted) return
      attempt.current = null; setUnknown(false); setReason(''); setRefresh(n => n + 1)
    } catch (err) { if (!signal.aborted) { setUnknown(attempt.current !== null); setError(err instanceof CompensationError ? err.message : '准备结果未确认，请保留本次请求重试。') } }
    finally { if (!signal.aborted) { busy.current = false; setPending(false) } }
  }
  return <section aria-label="处置与补偿计划">
    <h4>处置与补偿计划</h4><p>读取原回执和当前配置，生成独立计划。准备计划不执行恢复；人工事项不会自动删除或激活资源。</p>
    {error && <p role="alert" className="error-message">{error}</p>}
    {unknown && <button type="button" disabled={pending} onClick={() => void prepare()}>重试本次补偿准备</button>}
    <button type="button" onClick={() => { setError(''); setRefresh(n => n + 1) }}>刷新处置计划</button>
    <form className="plan-form" onSubmit={e => void prepare(e)}><fieldset disabled={!canPrepare || pending || unknown || !run}>
      <label>原执行记录<select value={run?.id ?? ''} onChange={e => setSelected(e.target.value)}><option value="">请选择已停止或完成的执行</option>{settled.map(r => <option key={r.id} value={r.id}>{r.id} · {r.state}</option>)}</select></label>
      <label>处置理由<input required maxLength={1024} value={reason} onChange={e => setReason(e.target.value)} /></label>
      <button type="submit">准备处置与补偿计划</button>
    </fieldset></form>
    {!canPrepare && <p>当前身份没有准备补偿计划的权限，仍可查看已有处置记录。</p>}
    {reports.map(report => <article className="snapshot-card" key={report.id} aria-label={`处置 ${report.id}`}>
      <h4>{labels[report.state] ?? report.state}</h4><p>{report.reason}</p><p>原执行：{report.sourceExecutionId}</p>
      {report.errorCode && <p>{reasons[report.errorCode] ?? '当前条件不允许执行补偿。'}</p>}
      <ul>{report.items.map(item => <li key={item.operationId}><strong>{item.key} · {labels[item.disposition] ?? item.disposition}</strong><p>{reasons[item.code] ?? '请人工核对处置方式。'}</p>{item.restoreValue && <p>当前值：{valueText(item.currentValue)} → 恢复值：{valueText(item.restoreValue)}</p>}</li>)}</ul>
      {report.derivedBundleId && onOpen && <button type="button" disabled={pending || unknown} onClick={() => onOpen(report.derivedBundleId!)}>打开补偿交付包：{report.derivedBundleId}</button>}
      {report.derivedBundleId && <p>此包只包含可执行的配置恢复，需重新批准；原回执和人工事项仍保留。</p>}
    </article>)}
  </section>
}
