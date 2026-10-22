import { useEffect, useRef, useState } from 'react'
import { useAPI } from './api'
import { parseFindingsContent, reviewHTTPError, ReviewResponseError, type FindingsReport, type ReviewExecution } from './review'

export default function ReviewFindings({ execution }: { execution: ReviewExecution }) {
  const api = useAPI()
  const controller = useRef<AbortController | null>(null)
  const [report, setReport] = useState<FindingsReport | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  useEffect(() => {
    setReport(null); setError(''); setPending(false)
    return () => controller.current?.abort()
  }, [api])

  async function read() {
    if (!execution.artifact || execution.tenantId !== api.tenantId) return
    controller.current?.abort()
    const current = new AbortController()
    controller.current = current
    setPending(true); setError(''); setReport(null)
    try {
      const query = new URLSearchParams({ tenantId: api.tenantId })
      const response = await api.fetch(`/api/v1/artifacts/${encodeURIComponent(execution.artifact.id)}/content?${query}`, { signal: current.signal })
      if (current.signal.aborted) return
      if (!response.ok) throw new ReviewResponseError((await reviewHTTPError(response)).message)
      const result = await parseFindingsContent(response, execution)
      if (!current.signal.aborted) setReport(result)
    } catch (error) {
      if (!current.signal.aborted) setError(error instanceof ReviewResponseError ? error.message : '无法读取审阅结果，请检查连接后重试。')
    } finally {
      if (!current.signal.aborted) setPending(false)
    }
  }

  return <section className="review-findings" aria-label={`${execution.taskId} 的审阅结果`}>
    <button type="button" disabled={pending} onClick={() => { void read() }}>{pending ? '正在读取结果…' : '读取审阅结果'}</button>
    {error && <p className="error-message" role="alert">{error}</p>}
    {report && (report.findings.length === 0
      ? <p>本轮未报告问题，仍需人工审阅。</p>
      : <><p>共 {report.findings.length} 条模型建议，待人工复核。</p>
        <ol aria-label="Findings">{report.findings.map((finding, index) => <li key={index}>
          <span className={`finding-severity severity-${finding.severity.toLowerCase()}`}>{finding.severity}</span>
          <h4>{finding.title}</h4>
          <code>{finding.path}:{finding.startLine}{finding.endLine !== finding.startLine ? `–${finding.endLine}` : ''}</code>
          <p className="finding-description">{finding.description}</p>
          <small>模型置信度：{Math.round(finding.confidence * 100)}%</small>
        </li>)}</ol></>)}
  </section>
}
