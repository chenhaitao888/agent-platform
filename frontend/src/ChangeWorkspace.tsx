import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import ChangeReviews from './ChangeReviews'
import ChangePlans from './ChangePlans'
import { useAPI } from './api'
import { changeErrors, type ChangeRequest, type ServiceEntry, type ServiceSelection, type SnapshotResult } from './change'

class ChangeAPIError extends Error {}

// Reset all state when an embedded consumer changes the authenticated API,
// including a different subject in the same tenant. Logout also unmounts it.
export default function ChangeWorkspace() {
  const api = useAPI()
  const [identity, setIdentity] = useState({ api, generation: 0 })
  if (identity.api !== api) setIdentity({ api, generation: identity.generation + 1 })
  return <ChangeSession key={identity.generation} />
}

function ChangeSession() {
  const api = useAPI()
  const [services, setServices] = useState<ServiceEntry[]>([])
  const [changes, setChanges] = useState<ChangeRequest[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [current, setCurrent] = useState<ChangeRequest | null>(null)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [chosenEnvironment, setEnvironment] = useState('')
  const [selections, setSelections] = useState<ServiceSelection[]>([])
  const [snapshotSelections, setSnapshotSelections] = useState<ServiceSelection[]>([])
  const [busy, setBusy] = useState(false)
  const [uncertainSnapshot, setUncertainSnapshot] = useState(false)
  const lifetime = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const createAttempt = useRef<{ signature: string; key: string } | null>(null)
  const snapshotAttempt = useRef<{ id: string; body: string } | null>(null)
  const environments = [...new Set(services.flatMap(s => s.environments))]
  const environment = chosenEnvironment || environments[0] || ''
  const updateReviewVersion = useCallback((version: number) => {
    setCurrent(previous => previous ? { ...previous, version: Math.max(previous.version, version) } : previous)
  }, [])

  async function read<T>(path: string, signal: AbortSignal, body?: string): Promise<T> {
    const response = await api.fetch(path, { signal, ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body } : {}) })
    if (!response.ok) {
      const value = await response.json().catch(() => ({})) as { error?: string }
      throw new ChangeAPIError(changeErrors[value.error ?? ''] ?? `请求失败（HTTP ${response.status}）`)
    }
    return await response.json() as T
  }

  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    async function load() {
      try {
        const [catalogResponse, changesResponse] = await Promise.all([
          api.fetch('/api/v1/services', { signal: controller.signal }),
          api.fetch('/api/v1/changes', { signal: controller.signal }),
        ])
        if (!catalogResponse.ok || !changesResponse.ok) throw new Error('load failed')
        const catalog = await catalogResponse.json() as { items: ServiceEntry[] }
        const list = await changesResponse.json() as { items: ChangeRequest[] }
        if (controller.signal.aborted) return
        if (!Array.isArray(catalog.items) || !Array.isArray(list.items)) throw new Error('invalid response')
        setServices(catalog.items); setChanges(list.items)
      } catch {
        if (!controller.signal.aborted) setError('读取服务目录或需求列表失败，请重新打开需求交付。')
      } finally { if (!controller.signal.aborted) setLoading(false) }
    }
    void load()
    return () => controller.abort()
  }, [api])

  function show(record: ChangeRequest) {
    setCurrent(record)
    setSnapshotSelections(record.services)
    snapshotAttempt.current = null; setUncertainSnapshot(false)
    setChanges(previous => [record, ...previous.filter(c => c.id !== record.id)])
  }

  async function open(id: string) {
    const signal = lifetime.current!.signal
    const attempt = ++sequence.current
    setCurrent(null); setError(''); snapshotAttempt.current = null; setUncertainSnapshot(false)
    try {
      const record = await read<ChangeRequest>(`/api/v1/changes/${encodeURIComponent(id)}`, signal)
      if (!signal.aborted && attempt === sequence.current) show(record)
    } catch (err) { if (!signal.aborted && attempt === sequence.current) setError(err instanceof ChangeAPIError ? err.message : '读取需求失败，请重试') }
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const signal = lifetime.current!.signal
    setBusy(true); setError(''); ++sequence.current
    const content = { title: title.trim(), description: description.trim(), environment, services: selections }
    const signature = JSON.stringify(content)
    const key = createAttempt.current?.signature === signature ? createAttempt.current.key : crypto.randomUUID()
    createAttempt.current = { signature, key }
    try {
      const record = await read<ChangeRequest>('/api/v1/changes', signal, JSON.stringify({ ...content, idempotencyKey: key }))
      if (signal.aborted) return
      show(record); createAttempt.current = null; setTitle(''); setDescription(''); setSelections([])
    } catch (err) { if (!signal.aborted) setError(err instanceof ChangeAPIError ? err.message : '创建结果未确认；保持输入不变并重试可找回同一需求。') }
    finally { if (!signal.aborted) setBusy(false) }
  }

  async function freeze() {
    if (!current || busy) return
    const signal = lifetime.current!.signal
    setBusy(true); setError(''); ++sequence.current
    const attempt = snapshotAttempt.current ?? {
      id: current.id,
      body: JSON.stringify({ expectedVersion: current.version, services: snapshotSelections, idempotencyKey: crypto.randomUUID() }),
    }
    snapshotAttempt.current = attempt
    try {
      const result = await read<SnapshotResult>(`/api/v1/changes/${encodeURIComponent(attempt.id)}/snapshots`, signal, attempt.body)
      if (signal.aborted) return
      const next = { ...current, version: result.changeVersion, snapshots: [...current.snapshots.filter(s => s.id !== result.snapshot.id), result.snapshot],
        ...(result.snapshot.state === 'READY' ? { currentSnapshotId: result.snapshot.id, services: snapshotSelections } : {}) }
      show(next)
      // Failed attempts retain the requested branches for an explicit retry.
      setSnapshotSelections(result.snapshot.services.map(s => ({ serviceId: s.serviceId, sourceBranch: s.sourceBranch, targetBranch: s.targetBranch })))
    } catch (err) {
      if (signal.aborted) return
      if (err instanceof ChangeAPIError) { snapshotAttempt.current = null; setUncertainSnapshot(false); setError(err.message) }
      else { setUncertainSnapshot(true); setError('快照结果未确认；重试本次请求会沿用同一标识和版本。') }
    } finally { if (!signal.aborted) setBusy(false) }
  }

  return <>
    <div className="workspace-heading"><div><span className="eyebrow">CHANGE PREPARATION</span><h1>需求交付</h1><p>选择服务与分支，为同一需求固定可追溯的代码版本。</p></div></div>
    {error && <p role="alert" className="error-message">{error}</p>}
    {loading ? <p role="status">正在读取需求与服务目录…</p> : <div className="change-layout">
      <section className="change-list" aria-label="需求列表">
        <h2>最近需求</h2>
        {changes.length === 0 ? <p>暂无需求</p> : <ul>{changes.map(c => <li key={c.id}>
          <button type="button" disabled={busy} onClick={() => void open(c.id)}>{c.title} · {c.id}</button>
          <small>{c.environment} · {c.services.length} 个服务</small>
        </li>)}</ul>}
        <form className="change-form" onSubmit={event => void create(event)} aria-label="创建需求">
          <h2>创建需求</h2>
          {services.length === 0 && <p>当前身份暂无可用服务，请由管理员登记服务目录和权限。</p>}
          <fieldset disabled={busy || services.length === 0}>
            <label>需求名称或编号<input value={title} onChange={e => setTitle(e.target.value)} maxLength={256} required /></label>
            <label>验收说明<textarea value={description} onChange={e => setDescription(e.target.value)} maxLength={4096} rows={3} /></label>
            <label>目标环境<select value={environment} onChange={e => { setEnvironment(e.target.value); setSelections([]) }}>{environments.map(env => <option key={env}>{env}</option>)}</select></label>
            {services.filter(s => s.environments.includes(environment)).map(service => {
              const selected = selections.find(s => s.serviceId === service.serviceId)
              return <div className="service-choice" key={service.serviceId}>
                <label className="service-checkbox"><input type="checkbox" checked={!!selected} aria-label={`选择 ${service.name}`} onChange={e => setSelections(previous => e.target.checked
                  ? [...previous, { serviceId: service.serviceId, sourceBranch: '', targetBranch: service.defaultTargetBranch }]
                  : previous.filter(s => s.serviceId !== service.serviceId))} />{service.name}</label>
                {selected && <BranchFields service={service} selection={selected} onChange={value => setSelections(previous => previous.map(s => s.serviceId === value.serviceId ? value : s))} />}
              </div>
            })}
            <button className="button-primary" type="submit" disabled={!selections.length || busy}>创建需求</button>
          </fieldset>
        </form>
      </section>
      <section className="change-detail" aria-label="需求详情">
        {!current ? <p>创建或选择一个需求，查看服务与代码快照。</p> : <>
          <div className="section-heading"><h2>{current.title}</h2><button type="button" disabled={busy} onClick={() => void open(current.id)}>重新读取详情</button></div>
          <p>{current.environment} · {current.services.length} 个服务 · 版本 {current.version}</p>
          <p className="change-description">{current.description}</p>
          {current.snapshots.length === 0 && <p className="next-step">尚未固定快照</p>}
          <form className="change-form" onSubmit={event => { event.preventDefault(); void freeze() }}>
            <fieldset disabled={busy || uncertainSnapshot}>
              <legend>本次快照的分支</legend>
              {snapshotSelections.map(selection => {
                const service = services.find(s => s.serviceId === selection.serviceId)
                return service && <div className="service-choice" key={service.serviceId}><strong>{service.name}</strong><BranchFields prefix="快照 " service={service} selection={selection} onChange={value => setSnapshotSelections(previous => previous.map(s => s.serviceId === value.serviceId ? value : s))} /></div>
              })}
            </fieldset>
            <p className="field-help">固定快照将读取所有服务的当前分支。已有快照与审阅结果保持不变。</p>
            <button className="button-primary" type="submit" disabled={busy}>{busy ? '处理中…' : uncertainSnapshot ? '重试本次快照' : current.snapshots.length ? '创建新快照' : '固定代码快照'}</button>
          </form>
          <div className="snapshot-history" aria-label="快照历史">{[...current.snapshots].reverse().map(snapshot => <article className="snapshot-card" key={snapshot.id}>
            <h3>快照 {snapshot.revision}</h3>
            <p className={snapshot.state === 'READY' ? 'next-step' : 'error-message'}>{snapshot.state === 'READY' ? '快照已固定' : '快照未完成 · 请检查失败服务后创建新快照'}</p>
            {snapshot.id === current.currentSnapshotId && <p className="field-help">当前可用快照</p>}
            <ul>{snapshot.services.map(service => <li key={service.serviceId}>
              <strong>{services.find(s => s.serviceId === service.serviceId)?.name ?? service.serviceId}</strong>
              <p>{service.sourceBranch} → {service.targetBranch}</p>
              {service.errorCode ? <p className="error-message">{changeErrors[service.errorCode] ?? '服务解析失败'}</p> : <p>版本已解析{snapshot.state === 'FAILED' ? '（本次整体快照未完成）' : ''}</p>}
              <details className="technical-details"><summary>版本与解析信息</summary><dl className="metadata">
                <div><dt>目录版本</dt><dd>{service.catalogVersion}</dd></div>
                <div><dt>解析时间</dt><dd>{service.resolvedAt}</dd></div>
                {service.repository && <><div><dt>源提交</dt><dd>{service.repository.headSha}</dd></div><div><dt>目标提交</dt><dd>{service.repository.targetSha}</dd></div><div><dt>共同祖先（merge-base）</dt><dd>{service.repository.baseSha}</dd></div></>}
              </dl></details>
            </li>)}</ul>
            <details className="technical-details"><summary>快照标识</summary><p>{current.id} / {snapshot.id}</p><code>{snapshot.inputHash}</code></details>
          </article>)}</div>
          {current.currentSnapshotId && <ChangeReviews key={current.id} change={current} onVersion={updateReviewVersion} onBusy={setBusy} blocked={busy} />}
          {current.currentSnapshotId && <ChangePlans key={`plans:${current.id}`} change={current} onVersion={updateReviewVersion} onBusy={setBusy} blocked={busy} />}
        </>}
      </section>
    </div>}
  </>
}

function BranchFields({ service, selection, onChange, prefix = '' }: { service: ServiceEntry; selection: ServiceSelection; onChange: (value: ServiceSelection) => void; prefix?: string }) {
  return <div className="branch-fields">
    <label>{prefix}{service.name} 源分支<input value={selection.sourceBranch} maxLength={256} required placeholder="feature/requirement" onChange={e => onChange({ ...selection, sourceBranch: e.target.value })} /></label>
    <label>{prefix}{service.name} 目标分支<select value={selection.targetBranch} onChange={e => onChange({ ...selection, targetBranch: e.target.value })}>{service.allowedTargetBranches.map(branch => <option key={branch}>{branch}</option>)}</select></label>
  </div>
}
