import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useAPI } from './api'
import type { DeliveryBundle } from './delivery'

type Approval = { id: string; tenantId: string; bundleId: string; mode: 'SIMULATION'; subject: string; reason: string; state: string; version: number; expiresAt: string; consumedBy?: string; errorCode?: string }
type Operation = { id: string; actionId: string; state: string; attempts: number; fence: number; receiptId?: string; errorCode?: string }
type Run = { id: string; tenantId: string; bundleId: string; approvalId: string; scenario: string; state: string; errorCode?: string; operations: Operation[] }
const labels: Record<string, string> = { APPROVED: '可用于模拟', CONSUMED: '已用于模拟', EXPIRED: '已过期', REVOKED: '已撤回', INVALIDATED: '已失效', QUEUED: '等待调度', RUNNING: '模拟进行中', SIMULATED: '模拟完成', BLOCKED: '已阻断', FAILED: '模拟失败', NOT_STARTED: '尚未开始', UNKNOWN: '等待回执核对', SUCCEEDED: '已核对模拟回执' }
const errors: Record<string, string> = { simulation_forbidden: '当前身份无权执行此操作，或可信绑定已变化。', simulation_unready: '请先处理计划中的阻断项，并核对 Findings。', approval_inactive: '批准已过期、撤回或使用，请刷新后确认。', archive_conflict: '请求标识或交付包摘要不一致，请重新读取。', version_conflict: '批准状态已变化，请刷新后操作。', validation_error: '请检查理由、有效期和确认选项。', archive_unavailable: '存储暂时不可用，请保留本次请求重试。', approval_expired: '批准已过期，未开始的操作已停止。', approval_revoked: '批准已撤回，未开始的操作已停止。', authorization_changed: '执行权限已变化，未开始的操作已停止。', policy_changed: '授权或连接策略已变化，需要重新确认。', resource_or_repository_drift: '分支、资源版本或连接能力已变化，请生成并保存新计划。', simulated_failure: '演练按选定场景中途失败，已完成的结果保留。', simulated_response_lost: '已模拟响应丢失，正在核对原回执。', invalid_saved_input: '已保存输入无法继续，请检查交付包完整性。' }
class SimulationError extends Error { uncertain: boolean; constructor(message: string, uncertain = false) { super(message); this.uncertain = uncertain } }

export default function SimulationPanel({ bundle }: { bundle: DeliveryBundle }) {
  const api = useAPI()
  const [approvals, setApprovals] = useState<Approval[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [reason, setReason] = useState('')
  const [ttl, setTTL] = useState(3600)
  const [reviewed, setReviewed] = useState(false)
  const [selected, setSelected] = useState('')
  const [scenario, setScenario] = useState('success')
  const [revokeReason, setRevokeReason] = useState('')
  const [pending, setPending] = useState(false)
  const [uncertain, setUncertain] = useState(false)
  const [available, setAvailable] = useState(true)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const lifetime = useRef<AbortController | null>(null)
  const busy = useRef(false)
  const attempt = useRef<{ path: string; body: string } | null>(null)
  const root = `/api/v1/deliveries/${encodeURIComponent(bundle.id)}`
  const active = runs.some(r => r.state === 'QUEUED' || r.state === 'RUNNING')
  const usable = approvals.filter(a => a.state === 'APPROVED' && !runs.some(r => r.approvalId === a.id))
  const approvalId = usable.some(a => a.id === selected) ? selected : usable[0]?.id ?? ''

  useEffect(() => { const controller = new AbortController(); lifetime.current = controller; return () => controller.abort() }, [api, bundle.id])
  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      try {
        const responses = await Promise.all([api.fetch(`${root}/simulation-approvals`, { signal: controller.signal }), api.fetch(`${root}/simulations`, { signal: controller.signal })])
        for (const response of responses) {
          if (!response.ok) {
            const data = await response.json().catch(() => ({})) as { error?: string }
            if (data.error === 'simulation_unavailable') { if (!controller.signal.aborted) setAvailable(false); return }
            throw new SimulationError(errors[data.error ?? ''] ?? '无法读取模拟状态。')
          }
        }
        const a = await responses[0].json() as { items: Approval[] }
        const r = await responses[1].json() as { items: Run[] }
        if (!Array.isArray(a.items) || !Array.isArray(r.items) || a.items.some(v => v.tenantId !== api.tenantId || v.bundleId !== bundle.id || v.mode !== 'SIMULATION') || r.items.some(v => v.tenantId !== api.tenantId || v.bundleId !== bundle.id)) throw new Error('mismatched simulation')
        if (!controller.signal.aborted) { setApprovals(a.items); setRuns(r.items); setAvailable(true) }
      } catch (err) { if (!controller.signal.aborted) setError(err instanceof SimulationError ? err.message : '无法读取与当前身份一致的模拟记录。') }
      finally { if (!controller.signal.aborted) setLoading(false) }
    }
    void load(); return () => controller.abort()
  }, [api, root, bundle.id, refresh])
  useEffect(() => { if (!active) return; const timer = setInterval(() => setRefresh(v => v + 1), 2000); return () => clearInterval(timer) }, [active])

  async function perform(path?: string, fields?: object) {
    if (busy.current || !lifetime.current) return
    const signal = lifetime.current.signal
    const request = attempt.current ?? { path: path!, body: JSON.stringify({ ...fields, idempotencyKey: crypto.randomUUID() }) }
    if (!request.path) return
    attempt.current = request; busy.current = true; setPending(true); setError('')
    try {
      const response = await api.fetch(request.path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: request.body })
      if (!response.ok) { const data = await response.json().catch(() => ({})) as { error?: string }; throw new SimulationError(errors[data.error ?? ''] ?? `操作未完成（HTTP ${response.status}）`, response.status >= 500) }
      const result = await response.json() as { id: string; tenantId: string; bundleId: string }
      if (!result.id || result.tenantId !== api.tenantId || result.bundleId !== bundle.id) throw new Error('invalid response')
      if (signal.aborted) return
      attempt.current = null; setUncertain(false); setReviewed(false); setRefresh(v => v + 1)
    } catch (err) {
      if (signal.aborted) return
      const unknown = !(err instanceof SimulationError) || err.uncertain
      setError(err instanceof SimulationError ? err.message : '结果未确认，请重试本次操作或刷新记录。')
      setUncertain(unknown); if (!unknown) attempt.current = null
    } finally { if (!signal.aborted) { busy.current = false; setPending(false) } }
  }
  function approve(event: FormEvent) { event.preventDefault(); void perform(`${root}/simulation-approvals`, { expectedDigest: bundle.digest, ttlSeconds: ttl, reason, findingsReviewed: reviewed }) }

  return <section className="simulation-panel" aria-label="交付模拟">
    <h3>交付模拟</h3><p>批准只适用于当前交付包的模拟。系统会重新核对权限、分支和资源版本，记录模拟回执；不会修改配置或激活实验。</p>
    {loading && <p role="status">正在读取模拟状态…</p>}
    {!available && <p>当前部署尚未启用模拟执行。</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {uncertain && <button type="button" disabled={pending} onClick={() => void perform()}>重试本次模拟操作</button>}
    <button type="button" onClick={() => { setError(''); setRefresh(v => v + 1) }}>刷新模拟记录</button>
    {available && <>
      <form className="plan-form" onSubmit={approve} aria-label="批准交付模拟">
        <fieldset disabled={pending || uncertain || loading || bundle.plan.state !== 'READY_FOR_REVIEW'}>
          <label>模拟批准理由<input required maxLength={1024} value={reason} onChange={e => setReason(e.target.value)} /></label>
          <label>批准有效期<select value={ttl} onChange={e => setTTL(Number(e.target.value))}><option value={900}>15 分钟</option><option value={3600}>1 小时</option><option value={86400}>24 小时</option></select></label>
          <label className="service-checkbox"><input type="checkbox" required checked={reviewed} onChange={e => setReviewed(e.target.checked)} />已核对 Findings 和变更计划，仅批准模拟</label>
          <button type="submit" className="button-primary">批准模拟</button>
        </fieldset>
      </form>
      {bundle.plan.state !== 'READY_FOR_REVIEW' && <p>此计划仍有待处理项，请先生成并保存可供审阅的新计划。</p>}
      <fieldset disabled={pending || uncertain || loading} className="simulation-start">
        <label>使用批准<select value={approvalId} onChange={e => setSelected(e.target.value)}><option value="">请选择未使用的有效批准</option>{usable.map(a => <option key={a.id} value={a.id}>{a.subject} · 到期 {a.expiresAt}</option>)}</select></label>
        <label>演练场景<select value={scenario} onChange={e => setScenario(e.target.value)}><option value="success">正常完成</option><option value="response_loss">响应丢失后核对</option><option value="fail_before">中途失败并停止后续</option></select></label>
        <button type="button" disabled={!approvalId} onClick={() => void perform(`${root}/simulations`, { expectedDigest: bundle.digest, approvalId, scenario })}>发起模拟</button>
      </fieldset>
      {approvals.length > 0 && <div className="simulation-approvals"><h4>批准记录</h4><label>撤回理由<input maxLength={1024} value={revokeReason} disabled={pending || uncertain} onChange={e => setRevokeReason(e.target.value)} /></label>{approvals.map(a => <div key={a.id}><p>{a.subject} · {labels[a.state]} · 到期 {a.expiresAt}</p><p>{a.reason}</p>{(a.state === 'APPROVED' || (a.state === 'CONSUMED' && runs.some(r => r.approvalId === a.id && r.state === 'RUNNING'))) && <button type="button" disabled={pending || uncertain || !revokeReason.trim()} onClick={() => void perform(`${root}/simulation-approvals/${encodeURIComponent(a.id)}/revoke`, { expectedVersion: a.version, reason: revokeReason })}>撤回模拟批准：{a.id}</button>}</div>)}</div>}
      {runs.map(run => <article className="snapshot-card" key={run.id} aria-label={`模拟 ${run.id}`}><h4>{labels[run.state]}{run.state === 'SIMULATED' ? ' · 未写入外部平台' : ''}</h4>{run.state === 'QUEUED' && <p>请求已持久保存，等待 Worker 调度和核验。</p>}{run.errorCode && <p className="error-message">{errors[run.errorCode] ?? '模拟需要人工核对。'}</p>}<ul>{run.operations.map(op => <li key={op.id}><strong>{bundle.plan.actions.find(a => a.id === op.actionId)?.key ?? op.actionId}</strong><p>{labels[op.state]}</p>{op.errorCode && <p>{errors[op.errorCode] ?? '此步骤需要核对。'}</p>}<details className="technical-details"><summary>操作与回执</summary><p>{op.id} · 尝试 {op.attempts} · 代次 {op.fence}</p><p>{op.receiptId ?? '尚无已核对回执'}</p></details></li>)}</ul></article>)}
    </>}
  </section>
}
