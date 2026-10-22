import { useEffect, useRef, useState } from 'react'
import { useAPI } from './api'

export default function ArtifactContent({ id, tenantId }: { id: string; tenantId: string }) {
  const api = useAPI()
  const controller = useRef<AbortController | null>(null)
  const [content, setContent] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  useEffect(() => () => controller.current?.abort(), [])
  async function read() {
    controller.current?.abort()
    const current = new AbortController()
    controller.current = current
    setPending(true)
    setError('')
    try {
      const query = new URLSearchParams({ tenantId })
      const response = await api.fetch(`/api/v1/artifacts/${encodeURIComponent(id)}/content?${query}`, { signal: current.signal })
      if (!response.ok) throw new Error('read failed')
      const text = await response.text()
      if (!current.signal.aborted) setContent(text.slice(0, 65_536))
    } catch {
      if (!current.signal.aborted) setError('无法读取 Artifact 内容，请检查身份权限。')
    } finally {
      if (!current.signal.aborted) setPending(false)
    }
  }
  return <div>
    <button type="button" disabled={pending} onClick={() => { void read() }}>读取 Artifact 内容</button>
    {error && <p role="alert">{error}</p>}
    {content !== null && <><p>内容预览（最多 65,536 个字符）</p><pre>{content}</pre></>}
  </div>
}
