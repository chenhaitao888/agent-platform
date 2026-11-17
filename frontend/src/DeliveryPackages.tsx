import { useEffect, useRef, useState } from 'react'
import { useAPI } from './api'
import { archiveErrors, type DeliveryBundle } from './delivery'
import { PlanPreview } from './ChangePlans'
import ReviewFindings from './ReviewFindings'
import { reviewFailureMessage } from './review'
import SimulationPanel from './SimulationPanel'

export default function DeliveryPackages() {
  const api = useAPI()
  const [identity, setIdentity] = useState({ api, generation: 0 })
  if (identity.api !== api) setIdentity({ api, generation: identity.generation + 1 })
  return <PackageSession key={identity.generation} />
}
function PackageSession() {
  const api = useAPI()
  const [items, setItems] = useState<DeliveryBundle[]>([])
  const [current, setCurrent] = useState<DeliveryBundle | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refresh, setRefresh] = useState(0)
  const lifetime = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  useEffect(() => {
    const controller = new AbortController(); lifetime.current = controller
    return () => controller.abort()
  }, [api])
  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      try {
        const response = await api.fetch('/api/v1/deliveries', { signal: controller.signal })
        if (!response.ok) { const data = await response.json() as { error?: string }; throw new Error(archiveErrors[data.error ?? ''] ?? '交付包列表读取失败。') }
        const data = await response.json() as { items: DeliveryBundle[] }
        if (!Array.isArray(data.items) || data.items.some(b => b.tenantId !== api.tenantId || b.schemaVersion !== 'delivery-archive.v1')) throw new Error('交付包列表与当前身份不一致。')
        if (!controller.signal.aborted) { setItems(data.items); setError('') }
      } catch { if (!controller.signal.aborted) setError('无法读取交付包。请检查身份、连接和交付包存储配置后重试。') }
      finally { if (!controller.signal.aborted) setLoading(false) }
    }
    void load(); return () => controller.abort()
  }, [api, refresh])
  async function open(id: string) {
    const signal = lifetime.current!.signal; const generation = ++sequence.current
    setCurrent(null); setError('')
    try {
      const response = await api.fetch(`/api/v1/deliveries/${encodeURIComponent(id)}`, { signal })
      if (!response.ok) throw new Error('unavailable')
      const bundle = await response.json() as DeliveryBundle
      if (bundle.id !== id || bundle.tenantId !== api.tenantId || bundle.schemaVersion !== 'delivery-archive.v1' || bundle.plan?.snapshotHash !== bundle.snapshot?.inputHash || bundle.plan?.tenantId !== api.tenantId) throw new Error('mismatched archive')
      if (!signal.aborted && generation === sequence.current) setCurrent(bundle)
    } catch { if (!signal.aborted && generation === sequence.current) setError('无法读取与当前身份一致的交付包，请重新选择或刷新列表。') }
  }
  return <>
    <div className="workspace-heading"><div><span className="eyebrow">SAVED DELIVERIES</span><h1>交付包</h1><p>回看已保存的需求计划、固定代码版本和审阅证据。</p></div></div>
    {error && <p className="error-message" role="alert">{error}</p>}
    <button type="button" onClick={() => { setLoading(true); setRefresh(v => v + 1) }}>刷新交付包列表</button>
    {loading ? <p role="status">正在读取交付包…</p> : <div className="change-layout">
      <section className="change-list" aria-label="交付包列表"><h2>已保存记录</h2>{items.length === 0 ? <p>暂无已保存的交付包</p> : <ul>{items.map(b => <li key={b.id}><button type="button" onClick={() => void open(b.id)}>{b.title} · {b.id}</button><small>{b.environment} · 计划 {b.plan.revision} · {b.createdAt}</small></li>)}</ul>}</section>
      <section className="change-detail" aria-label="交付包详情">{current ? <SavedPackage bundle={current} /> : <p>选择一份交付包查看保存时的计划与证据。</p>}</section>
    </div>}
  </>
}
function SavedPackage({ bundle }: { bundle: DeliveryBundle }) {
  return <>
    <h2>{bundle.title}</h2><p>保存人：{bundle.createdBy} · {bundle.createdAt}</p>
    <p>这是保存时的记录。当前代码和平台状态可能已变化，保存不代表批准或执行。</p>
    {bundle.description && <p className="change-description">{bundle.description}</p>}
    <details className="technical-details"><summary>固定代码版本与交付包摘要</summary><code>{bundle.digest}</code>{bundle.snapshot.services.map(s => <p key={s.serviceId}>{s.serviceId} · {s.sourceBranch} → {s.targetBranch}<br />{s.repository?.baseSha} → {s.repository?.headSha}</p>)}</details>
    <PlanPreview plan={bundle.plan} />
    <SimulationPanel key={bundle.id} bundle={bundle} />
    <section aria-label="已保存的审阅证据"><h3>审阅证据</h3>{bundle.evidence.length === 0 && <p>这份交付包没有已保存的审阅执行，具体缺失项见计划。</p>}{bundle.evidence.map(e => <article className="snapshot-card" key={e.serviceId}><h4>{e.serviceId} · {e.execution.state === 'SUCCEEDED' ? '审阅已完成' : '审阅未成功'}</h4>{e.execution.state === 'SUCCEEDED' ? <ReviewFindings key={`${bundle.id}:${e.execution.id}`} execution={e.execution} deliveryId={bundle.id} /> : <p>{reviewFailureMessage(e.execution.errorCode)}</p>}</article>)}</section>
  </>
}
