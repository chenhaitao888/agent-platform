import ArtifactContent from './ArtifactContent'
import TaskReview from './TaskReview'
import { useAPI } from './api'
import { useEffect, useRef, useState } from 'react'

import type { Artifact } from './artifact'
import type { WorkspaceDiff } from './review'
import type { Task } from './task'
import type { Workspace } from './workspace'

type WorkspaceDetailsProps = {
  task: Task
  autoReadReview?: boolean
}

type ErrorResponse = {
  message?: string
}

type WorkspaceState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'absent' }
  | { kind: 'registering' }
  | { kind: 'preparing' }
  | { kind: 'loaded'; workspace: Workspace }
  | { kind: 'failed'; message: string }

type DiffState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; diff: WorkspaceDiff }
  | { kind: 'failed'; message: string }

type ArtifactState =
  | { kind: 'idle' }
  | { kind: 'archiving' }
  | { kind: 'loaded'; artifact: Artifact }
  | { kind: 'failed'; message: string }

const maxDiffPreviewCharacters = 64 * 1024

export default function WorkspaceDetails({ task, autoReadReview = false }: WorkspaceDetailsProps) {
  const api = useAPI()

  const [state, setState] = useState<WorkspaceState>({ kind: 'idle' })
  const [diffState, setDiffState] = useState<DiffState>({ kind: 'idle' })
  const [artifactState, setArtifactState] = useState<ArtifactState>({
    kind: 'idle',
  })
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    controllerRef.current = controller
    setState({ kind: 'idle' })
    setDiffState({ kind: 'idle' })
    setArtifactState({ kind: 'idle' })
    // 一个 Task 的请求只属于这个 Task；切换时撤销，迟到的结果也不能写入新页面。
    return () => controller.abort()
  }, [task.id, task.tenantId])

  async function loadWorkspace() {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setState({ kind: 'loading' })
    setDiffState({ kind: 'idle' })
    setArtifactState({ kind: 'idle' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await api.fetch(
        `/api/v1/tasks/${task.id}/workspace?${query.toString()}`,
        { signal },
      )
      if (signal.aborted) return
      if (response.status === 404) {
        setState({ kind: 'absent' })
        return
      }
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        if (signal.aborted) return
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const workspace = (await response.json()) as Workspace
      if (signal.aborted) return
      setState({ kind: 'loaded', workspace })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function registerWorkspace() {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setState({ kind: 'registering' })
    setDiffState({ kind: 'idle' })
    setArtifactState({ kind: 'idle' })

    try {
      const response = await api.fetch(`/api/v1/tasks/${task.id}/workspace`, {
        method: 'POST',
        signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: `req_${crypto.randomUUID()}`,
          // 一个 Task 只登记一份 Workspace；重新加载页面后仍可重放同一登记操作。
          idempotencyKey: `workspace-register:v1:${task.id}`,
          tenantId: task.tenantId,
        }),
      })
      if (signal.aborted) return
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        if (signal.aborted) return
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const workspace = (await response.json()) as Workspace
      if (signal.aborted) return
      setState({ kind: 'loaded', workspace })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function prepareWorkspace(workspace: Workspace) {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setState({ kind: 'preparing' })
    setDiffState({ kind: 'idle' })
    setArtifactState({ kind: 'idle' })

    try {
      const response = await api.fetch(
        `/api/v1/tasks/${task.id}/workspace/prepare`,
        {
          method: 'POST',
          signal,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            requestId: `req_${crypto.randomUUID()}`,
            // 相同版本的重试沿用 key；真正失败后版本递增，才允许发起新一轮准备。
            idempotencyKey: `workspace-prepare:v1:${task.id}:v${workspace.version}`,
            tenantId: task.tenantId,
            expectedVersion: workspace.version,
          }),
        },
      )
      if (signal.aborted) return
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        if (signal.aborted) return
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const prepared = (await response.json()) as Workspace
      if (signal.aborted) return
      setState({ kind: 'loaded', workspace: prepared })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function loadDiff() {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setDiffState({ kind: 'loading' })
    setArtifactState({ kind: 'idle' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await api.fetch(
        `/api/v1/tasks/${task.id}/workspace/diff?${query.toString()}`,
        { signal },
      )
      if (signal.aborted) return
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        if (signal.aborted) return
        setDiffState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const diff = (await response.json()) as WorkspaceDiff
      if (signal.aborted) return
      setDiffState({ kind: 'loaded', diff })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setDiffState({ kind: 'failed', message })
    }
  }

  async function archiveDiff(workspace: Workspace) {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setArtifactState({ kind: 'archiving' })

    const requestId = `req_${crypto.randomUUID()}`
    const idempotencyKey = `artifact-diff:v1:${task.id}:${workspace.id}:v${workspace.version}`

    try {
      const response = await api.fetch(`/api/v1/tasks/${task.id}/artifacts/diff`, {
        method: 'POST',
        signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId,
          idempotencyKey,
          tenantId: task.tenantId,
          expectedWorkspaceVersion: workspace.version,
        }),
      })
      if (signal.aborted) return
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        if (signal.aborted) return
        setArtifactState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const artifact = (await response.json()) as Artifact
      if (signal.aborted) return
      setArtifactState({ kind: 'loaded', artifact })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setArtifactState({ kind: 'failed', message })
    }
  }

  const current = state.kind === 'loaded' ? state.workspace : null
  const busy = state.kind === 'loading' || state.kind === 'registering' || state.kind === 'preparing'

  return (
    <section className="workspace-details">
      <div className="workspace-entry">
        <div><strong>{current?.state === 'READY' ? '代码快照已就绪' : '审阅工作区'}</strong>
          {current?.state !== 'READY' && <p>准备代码快照，或读取这项任务已有的审阅。</p>}</div>
        <button type="button" className={current ? 'button-quiet' : 'button-primary'}
          aria-label={`打开审阅工作区：${task.id}`} disabled={busy} onClick={() => void loadWorkspace()}>
          {state.kind === 'loading' ? '正在读取…' : current ? '刷新工作区' : '打开审阅工作区'}
        </button>
      </div>
      {state.kind === 'failed' && <p className="error-message" role="alert">Workspace 操作失败：{state.message}</p>}
      {state.kind === 'absent' && <div className="workspace-registration" role="group" aria-label={`${task.id} 的仓库引用`}>
        <p className="next-step">这项任务尚未登记代码快照。先登记，再准备工作区。</p>
        <details className="technical-details"><summary>查看固定仓库版本</summary><dl className="metadata">
          <div><dt>仓库</dt><dd>{task.repository.provider} / {task.repository.repositoryId}</dd></div>
          <div><dt>目标分支</dt><dd>{task.repository.targetBranch}</dd></div>
          <div><dt>目标提交 SHA</dt><dd>{task.repository.targetSha}</dd></div>
          <div><dt>Base SHA</dt><dd>{task.repository.baseSha}</dd></div>
          <div><dt>Head SHA</dt><dd>{task.repository.headSha}</dd></div>
        </dl></details>
        <button className="button-primary" type="button" onClick={() => void registerWorkspace()}>登记 Workspace</button>
      </div>}
      {state.kind === 'registering' && <p className="loading-message" role="status">正在登记 Workspace…</p>}
      {state.kind === 'preparing' && <p className="loading-message" role="status">正在 clone 仓库并创建 worktree…</p>}
      {current && <div role="group" aria-label={`${task.id} 的 Workspace`}>
        <div className="workspace-reference">
          <span className={`workspace-state state-${current.state.toLowerCase()}`}>
            {current.state === 'READY' ? '快照就绪' : current.state === 'PREPARING' ? '准备中' : '待准备'} <code>{current.state}</code>
          </span>
          <span>{current.targetBranch} <span aria-hidden="true">←</span> <code>{current.headSha.slice(0, 8)}</code></span>
          <details className="technical-details"><summary>版本与工作目录</summary><dl className="metadata">
            <div><dt>仓库</dt><dd>{current.repository.provider} / {current.repository.repositoryId}</dd></div>
            <div><dt>目标分支</dt><dd>{current.targetBranch}</dd></div>
            <div><dt>目标提交 SHA</dt><dd>{current.targetSha}</dd></div>
            <div><dt>Base SHA</dt><dd>{current.baseSha}</dd></div>
            <div><dt>Head SHA</dt><dd>{current.headSha}</dd></div>
            {current.state === 'READY' && current.path && <div><dt>工作目录</dt><dd>{current.path}</dd></div>}
          </dl></details>
        </div>
        {current.state === 'REGISTERED' && <div className="next-step"><p>下一步：拉取固定版本的代码，为审阅准备工作区。</p>
          <button className="button-primary" type="button" onClick={() => void prepareWorkspace(current)}>准备 Workspace</button></div>}
        {current.state === 'PREPARING' && <p className="loading-message">Workspace 正在准备中，请稍后刷新。</p>}
        {current.state === 'READY' && <div className="review-workbench">
          <div className="diff-pane">
            <div className="pane-heading"><h3>代码差异</h3><button type="button" disabled={diffState.kind === 'loading'} onClick={() => void loadDiff()}>
              {diffState.kind === 'loading' ? '加载固定版本差异中…' : '查看固定版本差异'}</button></div>
            {diffState.kind === 'idle' && <div className="diff-placeholder"><span aria-hidden="true">±</span><strong>对照代码，理解改动</strong><p>读取固定版本的差异，与右侧审阅建议逐条核对。</p></div>}
            {diffState.kind === 'loading' && <p className="loading-message" role="status">正在读取代码差异…</p>}
            {diffState.kind === 'failed' && <p className="error-message" role="alert">差异加载失败：{diffState.message}</p>}
            {diffState.kind === 'loaded' && <section className="workspace-diff" aria-label={`${task.id} 的固定版本差异`}>
              <div className="diff-caption"><span>固定版本对比</span><span>{diffState.diff.sizeBytes} bytes</span></div>
              <pre>{diffState.diff.patch.slice(0, maxDiffPreviewCharacters)}</pre>
              {diffState.diff.patch.length > maxDiffPreviewCharacters && <p role="note">仅预览前 {maxDiffPreviewCharacters.toLocaleString('en-US')} 个字符；完整差异可归档后读取。</p>}
              <details className="technical-details"><summary>差异文件信息</summary><dl className="metadata">
                <div><dt>媒体类型</dt><dd>{diffState.diff.mediaType}</dd></div>
                <div><dt>SHA-256</dt><dd>{diffState.diff.sha256}</dd></div>
              </dl></details>
              <button type="button" disabled={artifactState.kind === 'archiving'} onClick={() => void archiveDiff(current)}>{artifactState.kind === 'archiving' ? '正在归档…' : '归档为 Artifact'}</button>
              {artifactState.kind === 'failed' && <p className="error-message" role="alert">Artifact 归档失败：{artifactState.message}</p>}
              {artifactState.kind === 'loaded' && <section className="workspace-artifact" aria-label={`${task.id} 的 Diff Artifact`}>
                <p className="success-message">差异已归档</p>
                <details className="technical-details"><summary>归档信息</summary><dl className="metadata">
                  <div><dt>Artifact ID</dt><dd>{artifactState.artifact.id}</dd></div>
                  <div><dt>类型</dt><dd>{artifactState.artifact.type}</dd></div>
                  <div><dt>SHA-256</dt><dd>{artifactState.artifact.sha256}</dd></div>
                </dl></details>
                <ArtifactContent key={artifactState.artifact.id} id={artifactState.artifact.id} tenantId={task.tenantId} />
              </section>}
            </section>}
          </div>
          <TaskReview task={task} workspace={current} autoRead={autoReadReview} />
        </div>}
      </div>}
    </section>
  )
}
