import { useState } from 'react'

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

export default function WorkspaceDetails({ task }: WorkspaceDetailsProps) {
  const [state, setState] = useState<WorkspaceState>({ kind: 'idle' })
  const [diffState, setDiffState] = useState<DiffState>({ kind: 'idle' })

  async function loadWorkspace() {
    setState({ kind: 'loading' })
    setDiffState({ kind: 'idle' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await fetch(
        `/api/v1/tasks/${task.id}/workspace?${query.toString()}`,
      )
      if (response.status === 404) {
        setState({ kind: 'absent' })
        return
      }
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const workspace = (await response.json()) as Workspace
      setState({ kind: 'loaded', workspace })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function registerWorkspace() {
    setState({ kind: 'registering' })
    setDiffState({ kind: 'idle' })

    try {
      const response = await fetch(`/api/v1/tasks/${task.id}/workspace`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: `req_${crypto.randomUUID()}`,
          idempotencyKey: `workspace-register:${crypto.randomUUID()}`,
          tenantId: task.tenantId,
        }),
      })
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const workspace = (await response.json()) as Workspace
      setState({ kind: 'loaded', workspace })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function prepareWorkspace(workspace: Workspace) {
    setState({ kind: 'preparing' })
    setDiffState({ kind: 'idle' })

    try {
      const response = await fetch(
        `/api/v1/tasks/${task.id}/workspace/prepare`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            requestId: `req_${crypto.randomUUID()}`,
            idempotencyKey: `workspace-prepare:${crypto.randomUUID()}`,
            tenantId: task.tenantId,
            expectedVersion: workspace.version,
          }),
        },
      )
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const prepared = (await response.json()) as Workspace
      setState({ kind: 'loaded', workspace: prepared })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  async function loadDiff() {
    setDiffState({ kind: 'loading' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await fetch(
        `/api/v1/tasks/${task.id}/workspace/diff?${query.toString()}`,
      )
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setDiffState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const diff = (await response.json()) as WorkspaceDiff
      setDiffState({ kind: 'loaded', diff })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setDiffState({ kind: 'failed', message })
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
              <pre>{diffState.diff.patch}</pre>
            </section>
          )}
        </div>
      )}
    </section>
  )
}
