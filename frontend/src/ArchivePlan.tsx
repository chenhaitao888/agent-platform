import { useEffect, useRef, useState } from 'react'
import { useAPI } from './api'
import { archiveErrors, type DeliveryBundle } from './delivery'
import type { ChangePlan } from './planning'

class ArchiveError extends Error {
  constructor(message: string, readonly uncertain = false) { super(message) }
}
export default function ArchivePlan({ changeId, plan, blocked, onBusy }: { changeId: string; plan: ChangePlan; blocked: boolean; onBusy: (busy: boolean) => void }) {
  const api = useAPI()
  const [pending, setPending] = useState(false)
  const [uncertain, setUncertain] = useState(false)
  const [saved, setSaved] = useState<DeliveryBundle | null>(null)
  const [error, setError] = useState('')
  const attempt = useRef<string | null>(null)
  const lifetime = useRef<AbortController | null>(null)
  useEffect(() => {
    const controller = new AbortController(); lifetime.current = controller
    return () => controller.abort()
  }, [api, changeId, plan.id, plan.hash])

  async function save() {
    if (pending || blocked || saved || plan.state === 'RUNNING') return
    const signal = lifetime.current!.signal
    const body = attempt.current ?? JSON.stringify({ idempotencyKey: crypto.randomUUID(), expectedPlanHash: plan.hash })
    attempt.current = body; setPending(true); onBusy(true); setError('')
    try {
      const response = await api.fetch(`/api/v1/changes/${encodeURIComponent(changeId)}/plans/${encodeURIComponent(plan.id)}/archive`, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body })
      if (!response.ok) {
        const data = await response.json().catch(() => ({})) as { error?: string }
        throw new ArchiveError(archiveErrors[data.error ?? ''] ?? `保存失败（HTTP ${response.status}）`, response.status >= 500)
      }
      const bundle = await response.json() as DeliveryBundle
      if (bundle.schemaVersion !== 'delivery-archive.v1' || bundle.tenantId !== api.tenantId || bundle.changeId !== changeId || bundle.plan?.id !== plan.id || bundle.plan.hash !== plan.hash || bundle.snapshot?.id !== plan.snapshotId || !bundle.id || !/^[a-f0-9]{64}$/.test(bundle.digest)) throw new Error('invalid archive response')
      if (signal.aborted) return
      setSaved(bundle); setUncertain(false); attempt.current = null
    } catch (err) {
      if (signal.aborted) return
      if (err instanceof ArchiveError) {
        setError(err.message + (err.uncertain ? ' 重试会保留本次标识，以确认可能已保存的记录。' : ''))
        if (!err.uncertain) attempt.current = null
        setUncertain(err.uncertain)
      }
      else { setError('保存结果未确认；重试本次保存会使用同一标识。也可在交付包列表查找已保存的记录。'); setUncertain(true) }
    } finally { if (!signal.aborted) { setPending(false); onBusy(false) } }
  }
  return <section className="archive-plan" aria-label="保存交付包">
    <p>保存当前计划、代码快照和审阅证据，后续计划变化不会覆盖这份记录。待处理计划也可保存。</p>
    <button type="button" disabled={pending || blocked || !!saved || plan.state === 'RUNNING'} onClick={() => void save()}>{pending ? '正在保存交付包…' : uncertain ? '重试本次保存' : saved ? '交付包已保存' : '保存当前计划为交付包'}</button>
    {error && <p className="error-message" role="alert">{error}</p>}
    {saved && <><p role="status">已保存，可从“交付包”查看。保存不代表批准或执行。</p><details className="technical-details"><summary>保存标识</summary><p>{saved.id}</p><code>{saved.digest}</code></details></>}
  </section>
}
