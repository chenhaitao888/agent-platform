import { useEffect, useState } from 'react'

import TaskCreator from './TaskCreator'
import TaskEventTimeline from './TaskEventTimeline'
import WorkspaceDetails from './WorkspaceDetails'
import type { Task } from './task'

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
  const [updatingTaskID, setUpdatingTaskID] = useState<string | null>(null)
  const [updateError, setUpdateError] = useState<string | null>(null)

  function handleTaskCreated(task: Task) {
    setListState((current) => {
      const currentItems = current.kind === 'ready' ? current.items : []
      const otherItems = currentItems.filter((item) => item.id !== task.id)
      return { kind: 'ready', items: [task, ...otherItems] }
    })
  }

  async function queueTask(task: Task) {
    setUpdatingTaskID(task.id)
    setUpdateError(null)

    try {
      const response = await fetch(`/api/v1/tasks/${task.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: `req_${crypto.randomUUID()}`,
          idempotencyKey: `task-transition:${crypto.randomUUID()}`,
          tenantId: task.tenantId,
          expectedVersion: task.version,
          status: 'QUEUED',
        }),
      })
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setUpdateError(error.message ?? `HTTP ${response.status}`)
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
      setUpdateError(message)
    } finally {
      setUpdatingTaskID(null)
    }
  }

  useEffect(() => {
    const controller = new AbortController()

    async function loadTasks() {
      try {
        const response = await fetch('/api/v1/tasks', {
          signal: controller.signal,
        })
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`)
        }

        const body = (await response.json()) as TaskListResponse
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
        if (error instanceof DOMException && error.name === 'AbortError') {
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
                      disabled={updatingTaskID === task.id}
                      onClick={() => void queueTask(task)}
                    >
                      {updatingTaskID === task.id ? '准入中…' : '加入队列'}
                    </button>
                  )}
                </div>
                {task.status === 'QUEUED' && <WorkspaceDetails task={task} />}
                <TaskEventTimeline task={task} />
              </li>
            ))}
          </ul>
        )}

        {updateError && (
          <p className="error-message" role="alert">
            Task 更新失败：{updateError}
          </p>
        )}
      </section>
    </div>
  )
}
