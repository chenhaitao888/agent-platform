import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useAPI } from './api'
import { changeErrors, type ChangeRequest, type ChangeSnapshot } from './change'
import { coverageLabels, operationLabels, planIssues, valueText, type ChangePlan, type ExperimentSpec, type PlanRequirement, type PlannedValue } from './planning'

type ConfigDraft = { key: string; type: '' | PlannedValue['type']; text: string; secretRef: string; secretVersion: string; reason: string }
type ExperimentDraft = { key: string; audience: string; metrics: string; allocation: string; exclusionGroup: string; control: string; treatment: string; weight: number; reason: string }
type ServiceDraft = { serviceId: string; config: ConfigDraft[]; experiments: ExperimentDraft[]; noConfig: boolean; noExperiment: boolean; configReason: string; experimentReason: string }
class PlanRequestError extends Error {}

export default function ChangePlans({ change, onVersion, onBusy, blocked }: { change: ChangeRequest; onVersion: (version: number) => void; onBusy: (busy: boolean) => void; blocked: boolean }) {
  const [selected, setSelected] = useState('')
  const snapshot = change.snapshots.find(s => s.id === (selected || change.currentSnapshotId))
  return <section className="change-plans" aria-label="配置与实验计划">
    <h3>配置与实验计划</h3>
    <p>读取固定代码与平台现状，集中补齐业务值，预览配置和未激活实验草稿的变化。此步骤只生成计划。</p>
    <label>计划快照<select value={snapshot?.id ?? ''} disabled={blocked} onChange={event => setSelected(event.target.value)}>
      {change.snapshots.filter(s => s.state === 'READY').map(s => <option key={s.id} value={s.id}>快照 {s.revision}{s.id === change.currentSnapshotId ? '（当前）' : '（历史）'}</option>)}
    </select></label>
    {snapshot && snapshot.id !== change.currentSnapshotId && <p role="note">正在查看历史快照 {snapshot.revision} 的计划，不代表当前代码。</p>}
    {snapshot && <SnapshotPlans key={`${change.id}:${snapshot.id}`} change={change} snapshot={snapshot} onVersion={onVersion} onBusy={onBusy} blocked={blocked} />}
  </section>
}

function SnapshotPlans({ change, snapshot, onVersion, onBusy, blocked }: { change: ChangeRequest; snapshot: ChangeSnapshot; onVersion: (version: number) => void; onBusy: (busy: boolean) => void; blocked: boolean }) {
  const api = useAPI()
  const [plans, setPlans] = useState<ChangePlan[]>([])
  const [selectedPlan, setSelectedPlan] = useState('')
  const [drafts, setDrafts] = useState<ServiceDraft[]>(snapshot.services.map(s => ({ serviceId: s.serviceId, config: [], experiments: [], noConfig: false, noExperiment: false, configReason: '', experimentReason: '' })))
  const [pending, setPending] = useState(false)
  const [loading, setLoading] = useState(true)
  const [uncertain, setUncertain] = useState(false)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const controller = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const attempt = useRef<string | null>(null)
  const path = `/api/v1/changes/${encodeURIComponent(change.id)}/plans`
  const history = plans.filter(p => p.snapshotId === snapshot.id).reverse()
  const current = history.find(p => p.id === selectedPlan) ?? history[0]

  useEffect(() => {
    const lifetime = new AbortController(); controller.current = lifetime
    return () => lifetime.abort()
  }, [api, change.id, snapshot.id])
  useEffect(() => {
    const request = new AbortController()
    const generation = ++sequence.current
    async function load() {
      try {
        const response = await api.fetch(path, { signal: request.signal })
        if (!response.ok) throw new Error('unavailable')
        const data = await response.json() as { items: ChangePlan[] }
        if (!Array.isArray(data.items)) throw new Error('invalid response')
        if (!request.signal.aborted && generation === sequence.current) setPlans(data.items)
      } catch { if (!request.signal.aborted && generation === sequence.current) setError('读取计划历史失败，请刷新计划历史。') }
      finally { if (!request.signal.aborted && generation === sequence.current) setLoading(false) }
    }
    void load(); return () => request.abort()
  }, [api, path, refresh])

  function update(index: number, value: Partial<ServiceDraft>) { setDrafts(previous => previous.map((d, i) => i === index ? { ...d, ...value } : d)) }
  async function generate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending || blocked || loading) return
    const signal = controller.current!.signal
    const requirements: PlanRequirement[] = drafts.map(d => ({ serviceId: d.serviceId,
      ...(d.noConfig ? { configNotInvolved: d.configReason } : { config: d.config.map(c => ({ key: c.key, reason: c.reason, ...(c.type ? { desired: c.type === 'secretRef' ? { type: c.type, secretRef: c.secretRef, secretVersion: c.secretVersion } : { type: c.type, text: c.text } } : {}) })) }),
      ...(d.noExperiment ? { experimentNotInvolved: d.experimentReason } : { experiments: d.experiments.map(e => ({ key: e.key, reason: e.reason, desired: { variants: [{ name: e.control, weight: e.weight }, { name: e.treatment, weight: 100 - e.weight }], audience: e.audience, metrics: e.metrics.split(',').map(m => m.trim()).filter(Boolean), allocation: e.allocation, ...(e.exclusionGroup ? { exclusionGroup: e.exclusionGroup } : {}) } })) }),
    }))
    const body = attempt.current ?? JSON.stringify({ snapshotId: snapshot.id, expectedVersion: change.version, idempotencyKey: crypto.randomUUID(), requirements })
    attempt.current = body; ++sequence.current; setPending(true); onBusy(true); setError('')
    try {
      const response = await api.fetch(path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body })
      if (!response.ok) {
        const value = await response.json().catch(() => ({})) as { error?: string }
        throw new PlanRequestError(value.error === 'planning_unavailable' ? '当前部署尚未配置只读计划平台，请由管理员登记。' : changeErrors[value.error ?? ''] ?? `计划请求失败（HTTP ${response.status}）`)
      }
      const result = await response.json() as { changeId: string; changeVersion: number; plan: ChangePlan }
      if (result.changeId !== change.id || result.plan?.snapshotId !== snapshot.id || result.plan.snapshotHash !== snapshot.inputHash || result.plan.tenantId !== api.tenantId || !Number.isSafeInteger(result.changeVersion)) throw new Error('invalid response')
      if (signal.aborted) return
      setPlans(previous => [...previous.filter(p => p.id !== result.plan.id), result.plan]); setSelectedPlan(result.plan.id)
      onVersion(result.changeVersion); attempt.current = null; setUncertain(false)
    } catch (err) {
      if (signal.aborted) return
      if (err instanceof PlanRequestError) { attempt.current = null; setUncertain(false); setError(err.message) }
      else { setUncertain(true); setError('计划结果未确认；重试本次请求会沿用同一标识和输入，不重复读取平台。') }
    } finally { if (!signal.aborted) { setPending(false); onBusy(false) } }
  }
  return <>
    <form className="plan-form" onSubmit={event => void generate(event)} aria-label="补齐配置与实验要求">
      <p>可先直接分析。代码无法确定的业务值会列为待补项；确认不涉及时请写明依据。</p>
      <fieldset disabled={pending || blocked || uncertain}>
        {drafts.map((draft, index) => <details key={draft.serviceId} className="plan-requirements">
          <summary>{draft.serviceId} · 补充业务要求与覆盖依据</summary>
          <label className="service-checkbox"><input type="checkbox" checked={draft.noConfig} onChange={event => update(index, { noConfig: event.target.checked })} />{draft.serviceId} 确认不涉及配置</label>
          {draft.noConfig ? <label>{draft.serviceId} 配置不涉及依据<input required maxLength={1024} value={draft.configReason} onChange={e => update(index, { configReason: e.target.value })} /></label> : <>
            {draft.config.map((row, rowIndex) => <ConfigFields key={rowIndex} label={`${draft.serviceId} 配置 ${rowIndex + 1}`} row={row} onChange={value => update(index, { config: draft.config.map((c, i) => i === rowIndex ? value : c) })} onRemove={() => update(index, { config: draft.config.filter((_, i) => i !== rowIndex) })} />)}
            <button type="button" onClick={() => update(index, { config: [...draft.config, { key: '', type: '', text: '', secretRef: '', secretVersion: '', reason: '' }] })}>添加配置要求：{draft.serviceId}</button>
          </>}
          <label className="service-checkbox"><input type="checkbox" checked={draft.noExperiment} onChange={event => update(index, { noExperiment: event.target.checked })} />{draft.serviceId} 确认不涉及实验</label>
          {draft.noExperiment ? <label>{draft.serviceId} 实验不涉及依据<input required maxLength={1024} value={draft.experimentReason} onChange={e => update(index, { experimentReason: e.target.value })} /></label> : <>
            {draft.experiments.map((row, rowIndex) => <ExperimentFields key={rowIndex} label={`${draft.serviceId} 实验 ${rowIndex + 1}`} row={row} onChange={value => update(index, { experiments: draft.experiments.map((c, i) => i === rowIndex ? value : c) })} onRemove={() => update(index, { experiments: draft.experiments.filter((_, i) => i !== rowIndex) })} />)}
            <button type="button" onClick={() => update(index, { experiments: [...draft.experiments, { key: '', audience: '', metrics: '', allocation: 'weighted', exclusionGroup: '', control: 'control', treatment: 'treatment', weight: 50, reason: '' }] })}>添加实验草稿要求：{draft.serviceId}</button>
          </>}
        </details>)}
      </fieldset>
      <button type="submit" className="button-primary" disabled={pending || blocked || loading}>{pending ? '正在生成计划…' : uncertain ? '重试本次计划请求' : '分析并生成新计划'}</button>
    </form>
    <button type="button" disabled={pending} onClick={() => { setError(''); setRefresh(v => v + 1) }}>刷新计划历史</button>
    {loading && <p role="status">正在读取计划历史…</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {!loading && !current && <p>此快照尚未生成配置与实验计划。</p>}
    {history.length > 0 && <label>计划历史<select value={current?.id ?? ''} onChange={e => setSelectedPlan(e.target.value)}>{history.map(p => <option key={p.id} value={p.id}>计划 {p.revision} · {p.state === 'READY_FOR_REVIEW' ? '待人工审阅' : p.state === 'RUNNING' ? '生成中' : '待补齐或处理'}</option>)}</select></label>}
    {current && (current.tenantId !== api.tenantId || current.snapshotHash !== snapshot.inputHash ? <p role="alert">计划与当前身份或快照不一致，请重新读取需求。</p> : <PlanPreview plan={current} />)}
  </>
}

function ConfigFields({ label, row, onChange, onRemove }: { label: string; row: ConfigDraft; onChange: (value: ConfigDraft) => void; onRemove: () => void }) {
  return <div className="plan-fields">
    <label>{label} 名称<input required maxLength={128} value={row.key} onChange={e => onChange({ ...row, key: e.target.value })} /></label>
    <label>{label} 类型<select value={row.type} onChange={e => onChange({ ...row, type: e.target.value as ConfigDraft['type'], text: '' })}><option value="">期望值待确定</option><option value="string">字符串</option><option value="boolean">布尔值</option><option value="integer">整数</option><option value="number">数值</option><option value="secretRef">密钥引用</option></select></label>
    {row.type === 'secretRef' ? <><label>{label} 密钥引用<input required value={row.secretRef} onChange={e => onChange({ ...row, secretRef: e.target.value })} placeholder="vault/service/key" /></label><label>{label} 密钥版本<input required value={row.secretVersion} onChange={e => onChange({ ...row, secretVersion: e.target.value })} /></label></> : row.type && <label>{label} 期望值{row.type === 'boolean' ? <select required value={row.text} onChange={e => onChange({ ...row, text: e.target.value })}><option value="">请选择</option><option value="false">false</option><option value="true">true</option></select> : <input required={row.type !== 'string'} maxLength={4096} value={row.text} onChange={e => onChange({ ...row, text: e.target.value })} />}</label>}
    <label>{label} 理由<input required maxLength={1024} value={row.reason} onChange={e => onChange({ ...row, reason: e.target.value })} /></label>
    <button type="button" onClick={onRemove}>移除要求：{label}</button>
  </div>
}
function ExperimentFields({ label, row, onChange, onRemove }: { label: string; row: ExperimentDraft; onChange: (value: ExperimentDraft) => void; onRemove: () => void }) {
  return <div className="plan-fields">
    {([['key', '名称'], ['audience', '受众'], ['metrics', '指标（逗号分隔）'], ['control', '对照组名称'], ['treatment', '实验组名称'], ['exclusionGroup', '互斥组（可选）'], ['reason', '理由']] as const).map(([key, name]) => <label key={key}>{label} {name}<input required={key !== 'exclusionGroup'} maxLength={key === 'reason' ? 1024 : 256} value={row[key]} onChange={e => onChange({ ...row, [key]: e.target.value })} /></label>)}
    <label>{label} 对照组占比<input required type="number" min={0} max={100} value={row.weight} onChange={e => onChange({ ...row, weight: Number(e.target.value) })} /></label>
    <label>{label} 分配方式<select value={row.allocation} onChange={e => onChange({ ...row, allocation: e.target.value })}><option value="weighted">按分组权重</option><option value="equal">等比例</option></select></label>
    <p>仅准备未激活草稿，分组占比不会在此步骤产生流量。</p>
    <button type="button" onClick={onRemove}>移除要求：{label}</button>
  </div>
}

function experimentText(spec: ExperimentSpec) {
  return `${spec.audience} · ${spec.variants.map(v => `${v.name} ${v.weight}%`).join(' / ')} · 指标：${spec.metrics.join(', ')} · ${spec.allocation}${spec.exclusionGroup ? ` · 互斥组：${spec.exclusionGroup}` : ''}`
}

function PlanPreview({ plan }: { plan: ChangePlan }) {
  return <article className="plan-preview" aria-label={`计划 ${plan.revision} 预览`}>
    <h4>{plan.state === 'READY_FOR_REVIEW' ? '计划已整理 · 待人工审阅' : plan.state === 'RUNNING' ? '计划生成中' : '计划存在待补齐或阻断项'}</h4>
    <p>目标环境：{plan.environment}。没有写入配置或激活实验。</p>
    <ul className="plan-issues">{plan.issues.map((issue, index) => <li key={index}>{issue.serviceId} {issue.key}：{planIssues[issue.code] ?? '计划需要人工核对'}</li>)}</ul>
    {plan.services.map(source => <div key={source.serviceId} className="plan-source">
      <strong>{source.serviceId}</strong><p>配置：{coverageLabels[source.configCoverage]} · 实验：{coverageLabels[source.experimentCoverage]}</p>
      {source.configReason && <p>配置依据：{source.configReason}</p>}{source.experimentReason && <p>实验依据：{source.experimentReason}</p>}
      {source.candidates.map(candidate => <details key={`${candidate.kind}:${candidate.key}`}><summary>{candidate.kind === 'config' ? '配置' : '实验'}候选：{candidate.key}</summary>{candidate.evidence.map((e, i) => <p key={i}>{e.source === 'fixed-diff' ? `代码证据：${e.path}:${e.line}` : `业务依据：${e.reason}`}</p>)}</details>)}
    </div>)}
    {plan.actions.map(action => <section className="plan-action" key={action.id} aria-label={`${operationLabels[action.operation]} ${action.key}`}>
      <h5>{operationLabels[action.operation]} · {action.key}</h5>
      <p>目标：{action.target.resourceRef} / {action.target.environment}</p>
      <p>当前：{action.currentValue ? valueText(action.currentValue) : action.currentExperiment ? experimentText(action.currentExperiment) : action.mustNotExist ? '不存在' : '待核对'}</p>
      {action.proposals.map(proposal => <div key={proposal.serviceId}><strong>{proposal.serviceId} 的期望</strong>{action.kind === 'config' ? <p>{valueText(proposal.value)}</p> : proposal.experiment ? <p>{experimentText(proposal.experiment)}</p> : <p>实验业务字段待补齐</p>}</div>)}
      <p>影响：{{ draft: '保存草稿，后续发布另行处理', publish_required: '保存后仍需发布，不能据此判断应用已加载', immediate: '未来写入可能立即生效', inactive_draft: '未激活草稿，不分流', unknown: '平台生效语义待确认' }[action.effect] ?? '待核对'}</p>
      <details className="technical-details"><summary>前置条件与证据标识</summary><p>{action.mustNotExist ? '必须不存在' : `必须匹配版本 ${action.expectedVersion}`} · 资源版本 {action.resourceVersion}</p><code>{action.hash}</code></details>
    </section>)}
    <details className="technical-details"><summary>计划标识与版本</summary><p>{plan.snapshotId} / {plan.id} · {plan.createdAt}</p><code>{plan.hash}</code></details>
  </article>
}
