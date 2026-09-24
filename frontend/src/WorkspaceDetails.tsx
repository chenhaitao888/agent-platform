import { useEffect, useRef, useState } from 'react'

import type { Artifact } from './artifact'
import type { WorkspaceDiff } from './review'
import type { Task } from './task'
import type { Workspace } from './workspace'

type WorkspaceDetailsProps = {
  task: Task
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

export default function WorkspaceDetails({ task }: WorkspaceDetailsProps) {
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
      const response = await fetch(
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
      const response = await fetch(`/api/v1/tasks/${task.id}/workspace`, {
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
      const response = await fetch(
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
      const response = await fetch(
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
      const response = await fetch(`/api/v1/tasks/${task.id}/artifacts/diff`, {
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

  return (
    <section className="workspace-details">
      <button
        type="button"
        aria-label={`查看 ${task.id} 的 Workspace`}
        disabled={
          state.kind === 'loading' ||
          state.kind === 'registering' ||
          state.kind === 'preparing'
        }
        onClick={() => void loadWorkspace()}
      >
        {state.kind === 'loading' ? '加载 Workspace 中…' : '查看 Workspace'}
      </button>

      {state.kind === 'failed' && (
        <p className="error-message" role="alert">
          Workspace 操作失败：{state.message}
        </p>
      )}

      {state.kind === 'absent' && (
        <div
          className="workspace-registration"
          role="group"
          aria-label={`${task.id} 的仓库引用`}
        >
          <p>将使用 Task 创建时固定的仓库引用登记：</p>
          <dl>
            <div>
              <dt>仓库</dt>
              <dd>
                {task.repository.provider} / {task.repository.repositoryId}
              </dd>
            </div>
            <div>
              <dt>目标分支</dt>
              <dd>{task.repository.targetBranch}</dd>
            </div>
            <div>
              <dt>目标提交 SHA</dt>
              <dd>{task.repository.targetSha}</dd>
            </div>
            <div>
              <dt>Base SHA</dt>
              <dd>{task.repository.baseSha}</dd>
            </div>
            <div>
              <dt>Head SHA</dt>
              <dd>{task.repository.headSha}</dd>
            </div>
          </dl>
          <button type="button" onClick={() => void registerWorkspace()}>
            登记 Workspace
          </button>
        </div>
      )}

      {state.kind === 'registering' && (
        <p className="task-list-message" role="status">
          正在登记 Workspace…
        </p>
      )}


      {state.kind === 'preparing' && (
        <p className="task-list-message" role="status">
          正在 clone 仓库并创建 worktree…
        </p>
      )}

      {state.kind === 'loaded' && (
        <div role="group" aria-label={`${task.id} 的 Workspace`}>
          <dl>
            <div>
              <dt>状态</dt>
              <dd>{state.workspace.state}</dd>
            </div>
            <div>
              <dt>仓库</dt>
              <dd>
                {state.workspace.repository.provider} /{' '}
                {state.workspace.repository.repositoryId}
              </dd>
            </div>
            <div>
              <dt>目标分支</dt>
              <dd>{state.workspace.targetBranch}</dd>
            </div>
            <div>
              <dt>目标提交 SHA</dt>
              <dd>{state.workspace.targetSha}</dd>
            </div>
            <div>
              <dt>Base SHA</dt>
              <dd>{state.workspace.baseSha}</dd>
            </div>
            <div>
              <dt>Head SHA</dt>
              <dd>{state.workspace.headSha}</dd>
            </div>
            {state.workspace.state === 'READY' && state.workspace.path && (
              <div>
                <dt>工作目录</dt>
                <dd>{state.workspace.path}</dd>
              </div>
            )}
          </dl>
          {state.workspace.state === 'REGISTERED' && (
            <button
              type="button"
              onClick={() => void prepareWorkspace(state.workspace)}
            >
              准备 Workspace
            </button>
          )}
          {state.workspace.state === 'PREPARING' && (
            <p className="task-list-message">
              Workspace 正在准备中，请稍后刷新。
            </p>
          )}
          {state.workspace.state === 'READY' && (
            <button
              type="button"
              disabled={diffState.kind === 'loading'}
              onClick={() => void loadDiff()}
            >
              {diffState.kind === 'loading'
                ? '加载固定版本差异中…'
                : '查看固定版本差异'}
            </button>
          )}
          {diffState.kind === 'failed' && (
            <p className="error-message" role="alert">
              差异加载失败：{diffState.message}
            </p>
          )}
          {diffState.kind === 'loaded' && (
            <section
              className="workspace-diff"
              aria-label={`${task.id} 的固定版本差异`}
            >
              <dl>
                <div>
                  <dt>媒体类型</dt>
                  <dd>{diffState.diff.mediaType}</dd>
                </div>
                <div>
                  <dt>大小</dt>
                  <dd>{diffState.diff.sizeBytes} bytes</dd>
                </div>
                <div>
                  <dt>SHA-256</dt>
                  <dd>{diffState.diff.sha256}</dd>
                </div>
              </dl>
              <pre>{diffState.diff.patch.slice(0, maxDiffPreviewCharacters)}</pre>
              {diffState.diff.patch.length > maxDiffPreviewCharacters && (
                <p role="note">
                  仅预览前 {maxDiffPreviewCharacters.toLocaleString('en-US')} 个字符；
                  完整差异可归档后读取。
                </p>
              )}
              <button
                type="button"
                disabled={artifactState.kind === 'archiving'}
                onClick={() => void archiveDiff(state.workspace)}
              >
                {artifactState.kind === 'archiving'
                  ? '正在归档…'
                  : '归档为 Artifact'}
              </button>
              {artifactState.kind === 'failed' && (
                <p className="error-message" role="alert">
                  Artifact 归档失败：{artifactState.message}
                </p>
              )}
              {artifactState.kind === 'loaded' && (
                <section
                  className="workspace-artifact"
                  aria-label={`${task.id} 的 Diff Artifact`}
                >
                  <dl>
                    <div>
                      <dt>Artifact ID</dt>
                      <dd>{artifactState.artifact.id}</dd>
                    </div>
                    <div>
                      <dt>类型</dt>
                      <dd>{artifactState.artifact.type}</dd>
                    </div>
                    <div>
                      <dt>SHA-256</dt>
                      <dd>{artifactState.artifact.sha256}</dd>
                    </div>
                  </dl>
                  <a
                    href={`/api/v1/artifacts/${artifactState.artifact.id}/content?${new URLSearchParams({ tenantId: task.tenantId }).toString()}`}
                  >
                    读取 Artifact 内容
                  </a>
                </section>
              )}
            </section>
          )}
        </div>
      )}
    </section>
  )
}
