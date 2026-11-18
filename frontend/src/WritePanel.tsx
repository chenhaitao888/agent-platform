import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useAPI } from './api'
import type { DeliveryBundle } from './delivery'
import CompensationPlans from './CompensationPlans'
import CompanyStatus, { type CompanyOperation } from './CompanyStatus'

type Access = { tenantId: string; bundleId: string; canApprove: boolean; canExecute: boolean; canRevoke: boolean; canPrepareCompensation?: boolean }
type Approval = { id: string; tenantId: string; bundleId: string; mode: 'WRITE'; subject: string; reason: string; state: string; version: number; expiresAt: string; consumedBy?: string }
type Receipt = { id: string; status: string; resourceVersion?: string; itemVersion?: string; effect?: string; recordedAt: string }
type Operation = { id: string; actionId: string; state: string; errorCode?: string; intentHash?: string; receipt?: Receipt; verification?: { state: string; observedAt: string }; company?: CompanyOperation }
type Run = { id: string; tenantId: string; bundleId: string; mode: 'WRITE'; approvalId: string; state: string; errorCode?: string; evidenceHash?: string; operations: Operation[]; companyFlow?: boolean }
const labels: Record<string, string> = { APPROVED: '可用于写入', CONSUMED: '已用于写入', EXPIRED: '已过期', REVOKED: '已撤回', QUEUED: '等待执行', RUNNING: '正在执行', SUCCEEDED: '执行完成 · 已逐项核验', FAILED: '写入被拒绝', DRIFTED: '写入后发现状态变化', UNKNOWN: '结果待核对', BLOCKED: '已停止', NOT_STARTED: '尚未开始', DISPATCHED: '请求已发出', APPLIED: '已写入，待核验', VERIFIED: '已写入并核验', REJECTED: '写入被拒绝' }
const effects: Record<string, string> = { draft: '保存为配置草稿', publish_required: '保存后仍需发布', immediate: '保存即生效', inactive_draft: '创建或更新未激活实验草稿' }
const messages: Record<string, string> = { writes_unavailable: '当前部署尚未启用配置与实验写入。', write_forbidden: '当前身份无权操作，或服务与平台绑定已变化。', change_forbidden: '当前身份无权读取此交付包。', unauthenticated: '身份凭据已失效，请重新登录。', write_unready: '请先完成计划和 Findings 的复核。', approval_inactive: '批准已使用、撤回或过期，请刷新记录。', version_conflict: '批准状态已变化，请刷新后再操作。', archive_conflict: '请求标识或交付包摘要不一致，请重新读取。', validation_error: '请检查理由、有效期和确认选项。', write_busy: '原请求仍在处理中，请稍后只读核对。', write_not_unknown: '结果已有记录，请刷新查看。', authorization_or_input_changed: '权限、代码或资源条件已变化，剩余操作已停止。', writer_changed: '平台连接已变化，剩余操作已停止。', outcome_unknown: '尚未确认远端结果。只读核对不会重发写入请求。', verification_unavailable: '已收到写入回执，暂时无法完成回读核验。', reconciliation_unavailable: '当前连接无法查询原请求，请检查部署配置。', resource_changed: '平台状态已发生变化，原写入回执保留。', precondition_failed: '平台版本条件不再满足，写入被拒绝。', intent_expired: '请求已过期，平台拒绝写入。', unsupported: '平台不支持此操作。', value_rejected: '平台拒绝了计划中的值。', invalid_saved_input: '已保存的执行记录未通过检查。', archive_unavailable: '交付存储暂时不可用。' }
class WriteError extends Error { constructor(message: string, readonly uncertain = false, readonly code = '') { super(message) } }

export default function WritePanel({ bundle, onOpen }: { bundle: DeliveryBundle; onOpen?: (id: string) => void }) {
  const api = useAPI()
  const [identity, setIdentity] = useState({ api, id: bundle.id, digest: bundle.digest, generation: 0 })
  if (identity.api !== api || identity.id !== bundle.id || identity.digest !== bundle.digest) setIdentity({ api, id: bundle.id, digest: bundle.digest, generation: identity.generation + 1 })
  return <WriteSession key={identity.generation} bundle={bundle} onOpen={onOpen} />
}

function WriteSession({ bundle, onOpen }: { bundle: DeliveryBundle; onOpen?: (id: string) => void }) {
  const api = useAPI()
  const [access, setAccess] = useState<Access | null>(null)
  const [approvals, setApprovals] = useState<Approval[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [available, setAvailable] = useState(true)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [now, setNow] = useState(Date.now)
  const [reason, setReason] = useState('')
  const [ttl, setTTL] = useState(900)
  const [reviewed, setReviewed] = useState(false)
  const [confirmed, setConfirmed] = useState(false)
  const [selected, setSelected] = useState('')
  const [revokeReason, setRevokeReason] = useState('')
  const [pending, setPending] = useState(false)
  const [uncertain, setUncertain] = useState(false)
  const lifetime = useRef<AbortController | null>(null)
  const busy = useRef(false)
  const attempt = useRef<{ path: string; body: string } | null>(null)
  const root = `/api/v1/deliveries/${encodeURIComponent(bundle.id)}`
  const company = bundle.plan.actions.some(a => a.descriptor?.implementationVersion === 'mcp-company.v1')
  const active = runs.some(r => ['QUEUED', 'RUNNING', 'WAITING_COMPANY'].includes(r.state) || r.companyFlow && r.state === 'UNKNOWN')
  const ready = bundle.plan.state === 'READY_FOR_REVIEW' && bundle.plan.actions.length > 0
  const usable = approvals.filter(a => a.state === 'APPROVED' && Date.parse(a.expiresAt) > now && !runs.some(r => r.approvalId === a.id))
  const approvalId = usable.some(a => a.id === selected) ? selected : usable[0]?.id ?? ''

  useEffect(() => { const c = new AbortController(); lifetime.current = c; return () => c.abort() }, [])
  useEffect(() => {
    const c = new AbortController()
    async function load() {
      setLoading(true)
      try {
        const responses = await Promise.all(['write-access', 'write-approvals', 'write-executions'].map(path => api.fetch(`${root}/${path}`, { signal: c.signal })))
        for (const response of responses) if (!response.ok) {
          const data = await response.json().catch(() => ({})) as { error?: string }
          throw new WriteError(messages[data.error ?? ''] ?? '无法读取写入记录。', false, data.error)
        }
        const permission = await responses[0].json() as Access
        const a = await responses[1].json() as { items: Approval[] }
        const r = await responses[2].json() as { items: Run[] }
        const owned = (v: { tenantId: string; bundleId: string }) => v.tenantId === api.tenantId && v.bundleId === bundle.id
        if (!owned(permission) || !Array.isArray(a.items) || !Array.isArray(r.items) || a.items.some(v => !owned(v) || v.mode !== 'WRITE') || r.items.some(v => !owned(v) || v.mode !== 'WRITE' || !Array.isArray(v.operations))) throw new Error('mismatched write records')
        if (!c.signal.aborted) { setAccess(permission); setApprovals(a.items); setRuns(r.items); setAvailable(true); setNow(Date.now()) }
      } catch (err) {
        if (!c.signal.aborted) {
          setAccess(null); setApprovals([]); setRuns([])
          if (err instanceof WriteError && err.code === 'writes_unavailable') { setAvailable(false); setError('') }
          else setError(err instanceof WriteError ? err.message : '无法读取与当前身份一致的写入记录。')
        }
      } finally { if (!c.signal.aborted) setLoading(false) }
    }
    void load(); return () => c.abort()
  }, [api, root, bundle.id, refresh])
  useEffect(() => { if (!active) return; const timer = setInterval(() => setRefresh(n => n + 1), 2000); return () => clearInterval(timer) }, [active])
  useEffect(() => { if (!approvals.some(a => a.state === 'APPROVED')) return; const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer) }, [approvals])

  async function perform(path?: string, fields?: object, reconcile = false) {
    if (busy.current || !lifetime.current || lifetime.current.signal.aborted) return
    const signal = lifetime.current.signal
    const request = attempt.current ?? { path: path!, body: JSON.stringify(reconcile ? {} : { ...fields, idempotencyKey: crypto.randomUUID() }) }
    if (!request.path) return
    attempt.current = request; busy.current = true; setPending(true); setError('')
    try {
      const response = await api.fetch(request.path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: request.body })
      if (!response.ok) { const data = await response.json().catch(() => ({})) as { error?: string }; throw new WriteError(messages[data.error ?? ''] ?? `操作未完成（HTTP ${response.status}）`, response.status >= 500) }
      const result = await response.json() as { id: string; tenantId: string; bundleId: string; mode: string }
      if (!result.id || result.tenantId !== api.tenantId || result.bundleId !== bundle.id || result.mode !== 'WRITE') throw new Error('mismatched write result')
      if (signal.aborted) return
      attempt.current = null; setUncertain(false); setReviewed(false); setConfirmed(false)
    } catch (err) {
      if (signal.aborted) return
      const unknown = !(err instanceof WriteError) || err.uncertain
      setUncertain(unknown); if (!unknown) attempt.current = null
      setError(err instanceof WriteError ? err.message : '操作结果未确认。可用同一请求重试，或刷新记录查看。')
    } finally { if (!signal.aborted) { busy.current = false; setPending(false); setRefresh(n => n + 1) } }
  }
  function approve(event: FormEvent) { event.preventDefault(); void perform(`${root}/write-approvals`, { expectedDigest: bundle.digest, ttlSeconds: ttl, reason, findingsReviewed: reviewed }) }
  const disabled = pending || uncertain || loading

  return <section className="simulation-panel" aria-label={bundle.compensation ? '配置补偿执行' : '配置与实验写入'}>
    <h3>{bundle.compensation ? '配置补偿执行' : '配置与实验写入'}</h3><p>目标环境：<strong>{bundle.environment}</strong>。执行会修改配置或保存未激活的实验草稿。</p>
    {!available && <p>当前部署尚未启用配置与实验写入。</p>}
    {loading && <p role="status">正在读取写入状态…</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {uncertain && <button type="button" disabled={pending} onClick={() => void perform()}>重试本次写入操作</button>}
    <button type="button" onClick={() => { setError(''); setRefresh(n => n + 1) }}>刷新写入记录</button>
    {available && <>
      {company && <p>本次流程：保存并提单 → 公司审批 → 公司自动发布 → 核验测试环境。下面的批准只授权保存和提单，公司工单仍需由审批人处理。关闭页面不影响后台追踪。</p>}
      <p>{[...new Set(bundle.plan.actions.map(a => effects[a.effect] ?? a.effect))].join('；')}。实验草稿不会激活或参与分流。</p>
      <form className="plan-form" aria-label="批准配置与实验写入" onSubmit={approve}>
        <fieldset disabled={disabled || !ready || !access?.canApprove}>
          <label>写入批准理由<input required maxLength={1024} value={reason} onChange={e => setReason(e.target.value)} /></label>
          <label>写入批准有效期<select value={ttl} onChange={e => setTTL(Number(e.target.value))}><option value={900}>15 分钟</option><option value={3600}>1 小时</option></select></label>
          <label className="service-checkbox"><input type="checkbox" required checked={reviewed} onChange={e => setReviewed(e.target.checked)} />{bundle.compensation ? '已复核原回执、补偿值和人工事项，批准本补偿计划' : '已复核 Findings、配置值和实验定义，批准本交付包的写入'}</label>
          <button type="submit" className="button-primary">{bundle.compensation ? '批准补偿' : company ? '批准保存与提单' : '批准写入'}</button>
        </fieldset>
      </form>
      {!ready && <p>请先保存包含可执行动作的完整计划。</p>}
      {access && !access.canApprove && <p>当前身份不能批准此计划，请由有权限的批准者复核。</p>}
      <fieldset className="simulation-start" disabled={disabled || !ready || !access?.canExecute}>
        <label>使用写入批准<select value={approvalId} onChange={e => { setSelected(e.target.value); setConfirmed(false) }}><option value="">请选择有效批准</option>{usable.map(a => <option key={a.id} value={a.id}>{a.subject} · 到期 {a.expiresAt}</option>)}</select></label>
        <label className="service-checkbox"><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} />{bundle.compensation ? `确认执行 ${bundle.environment} 环境的配置补偿` : `确认执行 ${bundle.environment} 环境的配置与实验草稿变更`}</label>
        <button type="button" disabled={!approvalId || !confirmed} onClick={() => void perform(`${root}/write-executions`, { expectedDigest: bundle.digest, approvalId })}>{bundle.compensation ? '执行配置补偿' : company ? '保存配置并提交公司工单' : '执行配置与实验写入'}</button>
      </fieldset>
      {approvals.length > 0 && <div className="simulation-approvals"><h4>写入批准记录</h4><label>写入撤回理由<input maxLength={1024} value={revokeReason} disabled={disabled} onChange={e => setRevokeReason(e.target.value)} /></label>{approvals.map(a => {
        const expired = Date.parse(a.expiresAt) <= now && a.state !== 'REVOKED'
        return <div key={a.id}><p>{a.subject} · {labels[expired ? 'EXPIRED' : a.state] ?? a.state} · 到期 {a.expiresAt}</p><p>{a.reason}</p>{!expired && (a.state === 'APPROVED' || a.state === 'CONSUMED') && <button type="button" disabled={disabled || !access?.canRevoke || !revokeReason.trim()} onClick={() => void perform(`${root}/write-approvals/${encodeURIComponent(a.id)}/revoke`, { expectedVersion: a.version, reason: revokeReason })}>撤回写入批准：{a.id}</button>}</div>
      })}<p>撤回会停止尚未开始的动作；已发出的请求仍需核对结果。</p></div>}
      {runs.map(run => <article className="snapshot-card" key={run.id} aria-label={`写入 ${run.id}`}>
        <h4>{run.companyFlow && run.state === 'SUCCEEDED' ? '公司交付完成 · 测试环境已核验' : run.state === 'WAITING_COMPANY' ? '等待公司审批或自动发布' : run.companyFlow && run.state === 'FAILED' ? '公司交付未完成 · 请查看审批或发布结果' : bundle.compensation && run.state === 'SUCCEEDED' ? '补偿动作完成 · 已逐项核验' : labels[run.state] ?? run.state}</h4>
        {run.state === 'QUEUED' && <p>请求已保存，等待执行和条件核验。</p>}
        {run.errorCode && <p className="error-message">{messages[run.errorCode] ?? '此执行需要人工核对。'}</p>}
        {run.state === 'UNKNOWN' && <><p>剩余动作已停止。只读核对不会重发写入，也不会自动继续剩余动作。</p><button type="button" disabled={disabled || !access} onClick={() => void perform(`${root}/write-executions/${encodeURIComponent(run.id)}/reconcile`, {}, true)}>只读核对结果：{run.id}</button></>}
        <ul>{run.operations.map(op => <li key={op.id}><strong>{bundle.plan.actions.find(a => a.id === op.actionId)?.key ?? op.actionId}</strong><p>{op.company && op.state === 'VERIFIED' ? '配置已保存，保存结果已核验' : labels[op.state] ?? op.state}</p>{op.errorCode && <p>{messages[op.errorCode] ?? '此步骤需要核对。'}</p>}{op.company && <CompanyStatus operation={op.company} />}{op.receipt?.effect && <p>{op.company ? '保存方式：公司审批后自动发布' : effects[op.receipt.effect] ?? op.receipt.effect}</p>}<details className="technical-details"><summary>写入回执与核验</summary><p>{op.id}</p><p>{op.receipt ? `${op.receipt.id} · ${op.receipt.status} · ${op.receipt.recordedAt}` : '尚未取得远端回执'}</p>{op.receipt?.resourceVersion && <p>资源版本 {op.receipt.resourceVersion} · 条目版本 {op.receipt.itemVersion}</p>}{op.verification && <p>{op.verification.state} · {op.verification.observedAt}</p>}</details></li>)}</ul>
      </article>)}
      {!bundle.compensation && runs.some(r => !['QUEUED', 'RUNNING', 'WAITING_COMPANY'].includes(r.state)) && <CompensationPlans bundle={bundle} runs={runs.filter(r => r.state !== 'WAITING_COMPANY')} canPrepare={!!access?.canPrepareCompensation} onOpen={onOpen} />}
    </>}
  </section>
}
