import { useEffect, useRef, useState } from 'react'

import type { Task, TaskEvent } from './task'

type TaskEventTimelineProps = {
  task: Task
}

type TaskEventListResponse = {
  items: TaskEvent[]
}

type ErrorResponse = {
  message?: string
}

type TimelineState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'ready'; items: TaskEvent[] }
  | { kind: 'failed'; message: string }

function eventSummary(event: TaskEvent): string {
  switch (event.eventType) {
    case 'task.created':
    case 'task.queued':
      return `${event.payload.task.status} · version ${event.payload.task.version}`
    case 'workspace.registered':
    case 'workspace.preparing':
    case 'workspace.ready':
    case 'workspace.preparation_failed':
      return `${event.payload.workspace.state} · version ${event.payload.workspace.version} · ${event.payload.workspace.workspaceId}`
    case 'artifact.created':
      return `${event.payload.artifact.artifactId} · ${event.payload.artifact.type} · ${event.payload.artifact.sizeBytes} B`
  }
}

export default function TaskEventTimeline({ task }: TaskEventTimelineProps) {
  const [state, setState] = useState<TimelineState>({ kind: 'idle' })
  const controllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    controllerRef.current = controller
    setState({ kind: 'idle' })
    // 类似 Java 请求作用域结束时关闭资源：Task 一换，旧请求的结果就失效。
    return () => controller.abort()
  }, [task.id, task.tenantId])

  async function loadEvents() {
    const signal = controllerRef.current?.signal
    if (!signal || signal.aborted) return
    setState({ kind: 'loading' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await fetch(
        `/api/v1/tasks/${task.id}/events?${query.toString()}`,
        { signal },
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

      const body = (await response.json()) as TaskEventListResponse
      if (signal.aborted) return
      setState({ kind: 'ready', items: body.items })
    } catch (error) {
      if (signal.aborted) return
      const message = error instanceof Error ? error.message : 'unknown error'
      setState({ kind: 'failed', message })
    }
  }

  return (
    <section className="task-event-timeline">
      <button
        type="button"
        aria-label={`查看 ${task.id} 的事件`}
        disabled={state.kind === 'loading'}
        onClick={() => void loadEvents()}
      >
        {state.kind === 'loading' ? '加载事件中…' : '查看事件'}
      </button>

      {state.kind === 'failed' && (
        <p className="error-message" role="alert">
          事件加载失败：{state.message}
        </p>
      )}

      {state.kind === 'ready' && (
        <ol aria-label={`${task.id} 的事件时间线`}>
          {state.items.map((event) => (
            <li key={event.eventId}>
              <strong>{event.eventType}</strong>
              <span>{eventSummary(event)}</span>
              <time dateTime={event.occurredAt}>{event.occurredAt}</time>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
