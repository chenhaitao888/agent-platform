import { useState, type FormEvent } from 'react'

import { LOCAL_TENANT_ID, type Task } from './task'

type TaskCreatorProps = {
  onCreated?: (task: Task) => void
}

type ErrorResponse = {
  message?: string
}

export default function TaskCreator({ onCreated }: TaskCreatorProps) {
  const [taskType, setTaskType] = useState('PR_REVIEW')
  const [goal, setGoal] = useState('')
  const [createdTask, setCreatedTask] = useState<Task | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setIsSubmitting(true)
    setErrorMessage(null)

    try {
      const requestId = `req_${crypto.randomUUID()}`
      const idempotencyKey = `task-create:${crypto.randomUUID()}`
      const response = await fetch('/api/v1/tasks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId,
          idempotencyKey,
          tenantId: LOCAL_TENANT_ID,
          type: taskType,
          goal,
        }),
      })
      if (!response.ok) {
        const error = (await response.json()) as ErrorResponse
        setErrorMessage(error.message ?? `HTTP ${response.status}`)
        return
      }

      const task = (await response.json()) as Task
      setCreatedTask(task)
      onCreated?.(task)
    } catch (error) {
      const message = error instanceof Error ? error.message : 'unknown error'
      setErrorMessage(message)
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <section className="task-creator" aria-labelledby="create-task-title">
      <h2 id="create-task-title">创建 Task</h2>
      <p className="task-creator-description">
        提交一个最小研发任务，当前数据只保存在 Go 进程内存中。
      </p>

      <form onSubmit={handleSubmit}>
        <label htmlFor="task-type">任务类型</label>
        <select
          id="task-type"
          value={taskType}
          onChange={(event) => setTaskType(event.target.value)}
        >
          <option value="PR_REVIEW">PR Review</option>
          <option value="BUG_FIX">Bug Fix</option>
        </select>

        <label htmlFor="task-goal">任务目标</label>
        <textarea
          id="task-goal"
          value={goal}
          onChange={(event) => setGoal(event.target.value)}
          rows={4}
        />

        <button type="submit" disabled={isSubmitting}>
          {isSubmitting ? '创建中…' : '创建 Task'}
        </button>
      </form>

      {errorMessage && (
        <p className="error-message" role="alert">
          创建失败：{errorMessage}
        </p>
      )}

      {createdTask && (
        <section className="task-result" aria-label="创建结果">
          <p>
            Task ID：<strong>{createdTask.id}</strong>
          </p>
          <p>
            状态：<strong>{createdTask.status}</strong>
          </p>
          <p>{createdTask.goal}</p>
        </section>
      )}
    </section>
  )
}
