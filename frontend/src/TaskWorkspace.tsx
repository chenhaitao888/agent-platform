import { useEffect, useState } from 'react'

import TaskCreator from './TaskCreator'
import TaskEventTimeline from './TaskEventTimeline'
import WorkspaceDetails from './WorkspaceDetails'
import { LOCAL_TENANT_ID, type Task } from './task'

type TaskListResponse = {
  items: Task[]
}

type ErrorResponse = {
  message?: string
}

type TaskListState =
  | { kind: 'loading' }
  | { kind: 'ready'; items: Task[] }
  | { kind: 'failed'; message: string }

function mergeTasks(currentItems: Task[], serverItems: Task[]) {
  const currentByID = new Map(currentItems.map((task) => [task.id, task]))

  const mergedServerItems = serverItems.map((serverTask) => {
    const currentTask = currentByID.get(serverTask.id)
    currentByID.delete(serverTask.id)

    if (currentTask && currentTask.version > serverTask.version) {
      return currentTask
    }
    return serverTask
  })

  return [...currentByID.values(), ...mergedServerItems]
}

export default function TaskWorkspace() {
  const [listState, setListState] = useState<TaskListState>({ kind: 'loading' })
  const [updatingTaskIDs, setUpdatingTaskIDs] = useState<Set<string>>(() => new Set())
  const [updateErrors, setUpdateErrors] = useState<Map<string, string>>(() => new Map())

  function handleTaskCreated(task: Task) {
    setListState((current) => {
      const currentItems = current.kind === 'ready' ? current.items : []
      const otherItems = currentItems.filter((item) => item.id !== task.id)
      return { kind: 'ready', items: [task, ...otherItems] }
    })
  }

  async function queueTask(task: Task) {
    // 不同 Task 可以分别准入；每个请求只管理自己的按钮状态。
    setUpdatingTaskIDs((current) => new Set(current).add(task.id))
    setUpdateErrors((current) => {
      const remaining = new Map(current)
      remaining.delete(task.id)
      return remaining
    })

    try {
      const response = await fetch(`/api/v1/tasks/${task.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: `req_${crypto.randomUUID()}`,
          // 同一 Task 版本的准入是同一次操作；响应丢失后重试仍使用这个 key。
          idempotencyKey: `task-queue:v1:${task.id}:v${task.version}`,
          tenantId: task.tenantId,
          expectedVersion: task.version,
          status: 'QUEUED',
        }),
      })
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setUpdateErrors((current) =>
          new Map(current).set(task.id, error.message ?? `HTTP ${response.status}`),
        )
        return
      }

      const updated = (await response.json()) as Task
      setListState((current) => {
        if (current.kind !== 'ready') {
          return current
        }
        return {
          kind: 'ready',
          items: current.items.map((item) =>
            item.id === updated.id ? updated : item,
          ),
        }
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setUpdateErrors((current) => new Map(current).set(task.id, message))
    } finally {
      setUpdatingTaskIDs((current) => {
        const remaining = new Set(current)
        remaining.delete(task.id)
        return remaining
      })
    }
  }

  useEffect(() => {
    const controller = new AbortController()

    async function loadTasks() {
      try {
        const query = new URLSearchParams({ tenantId: LOCAL_TENANT_ID })
        const response = await fetch(`/api/v1/tasks?${query.toString()}`, {
          signal: controller.signal,
        })
        if (controller.signal.aborted) return
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`)
        }

        const body = (await response.json()) as TaskListResponse
        if (controller.signal.aborted) return
        setListState((current) => {
          if (current.kind !== 'ready') {
            return { kind: 'ready', items: body.items }
          }

          return {
            kind: 'ready',
            items: mergeTasks(current.items, body.items),
          }
        })
      } catch (error) {
        if (
          controller.signal.aborted ||
          (error instanceof DOMException && error.name === 'AbortError')
        ) {
          return
        }

        const message = error instanceof Error ? error.message : 'unknown error'
        setListState((current) =>
          current.kind === 'ready' ? current : { kind: 'failed', message },
        )
      }
    }

    void loadTasks()

    return () => controller.abort()
  }, [])

  return (
    <div className="task-workspace">
      <TaskCreator onCreated={handleTaskCreated} />

      <section className="task-list" aria-labelledby="task-list-title">
        <h2 id="task-list-title">最近 Task</h2>

        {listState.kind === 'loading' && (
          <p className="task-list-message" role="status">
            正在加载 Task…
          </p>
        )}

        {listState.kind === 'failed' && (
          <p className="error-message" role="alert">
            列表加载失败：{listState.message}
          </p>
        )}

        {listState.kind === 'ready' && listState.items.length === 0 && (
          <p className="task-list-message">暂无 Task</p>
        )}

        {listState.kind === 'ready' && listState.items.length > 0 && (
          <ul aria-label="最近 Task">
            {listState.items.map((task) => (
              <li key={task.id}>
                <div className="task-list-metadata">
                  <span>{task.type}</span>
                  <strong>{task.status}</strong>
                </div>
                <p>{task.goal}</p>
                <div className="task-list-footer">
                  <small>
                    {task.id} · version {task.version}
                  </small>
                  {task.status === 'CREATED' && (
                    <button
                      type="button"
                      aria-label={`将 ${task.id} 加入队列`}
                      disabled={updatingTaskIDs.has(task.id)}
                      onClick={() => void queueTask(task)}
                    >
                      {updatingTaskIDs.has(task.id) ? '准入中…' : '加入队列'}
                    </button>
                  )}
                </div>
                {updateErrors.has(task.id) && (
                  <p className="error-message" role="alert">
                    Task 更新失败：{updateErrors.get(task.id)}
                  </p>
                )}
                {task.status === 'QUEUED' && <WorkspaceDetails task={task} />}
                <TaskEventTimeline task={task} />
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
