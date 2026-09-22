import { useState } from 'react'

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

export default function TaskEventTimeline({ task }: TaskEventTimelineProps) {
  const [state, setState] = useState<TimelineState>({ kind: 'idle' })

  async function loadEvents() {
    setState({ kind: 'loading' })

    try {
      const query = new URLSearchParams({ tenantId: task.tenantId })
      const response = await fetch(
        `/api/v1/tasks/${task.id}/events?${query.toString()}`,
      )
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setState({
          kind: 'failed',
          message: error.message ?? `HTTP ${response.status}`,
        })
        return
      }

      const body = (await response.json()) as TaskEventListResponse
      setState({ kind: 'ready', items: body.items })
    } catch (error) {
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
              <span>
                {event.payload.status} · version {event.payload.version}
              </span>
              <time dateTime={event.occurredAt}>{event.occurredAt}</time>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
