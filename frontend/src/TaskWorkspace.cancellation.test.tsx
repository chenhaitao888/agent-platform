import { act, render, screen } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskWorkspace from './TaskWorkspace'
import type { Task } from './task'

afterEach(() => {
  vi.unstubAllGlobals()
})

function deferredResponse() {
  let resolve: (response: Response) => void = () => undefined
  let reject: (error: Error) => void = () => undefined
  const response = new Promise<Response>((complete, fail) => {
    resolve = complete
    reject = fail
  })
  return { response, resolve, reject }
}

function taskForTest(id: string, goal: string): Task {
  return {
    id,
    tenantId: 'tenant-local',
    idempotencyKey: `create-${id}`,
    type: 'PR_REVIEW',
    goal,
    repository: {
      provider: 'gitlab',
      repositoryId: 'platform/project-7',
      targetBranch: 'master',
      targetSha: '3'.repeat(40),
      baseSha: '1'.repeat(40),
      headSha: '2'.repeat(40),
    },
    status: 'CREATED',
    version: 1,
    createdAt: '2026-10-05T00:00:00Z',
  }
}

function listResponse(items: Task[]) {
  return Response.json({ items })
}

function renderListFixture() {
  const obsolete = deferredResponse()
  const current = deferredResponse()
  const signals: (AbortSignal | null | undefined)[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (input !== '/api/v1/tasks?tenantId=tenant-local') {
        return Promise.reject(new Error(`Unexpected request: ${String(input)}`))
      }
      signals.push(init?.signal)
      return signals.length === 1 ? obsolete.response : current.response
    }),
  )
  render(
    <StrictMode>
      <TaskWorkspace />
    </StrictMode>,
  )
  async function finishPendingRequests() {
    await act(async () => {
      obsolete.resolve(listResponse([]))
      current.resolve(listResponse([]))
    })
  }
  return { obsolete, current, signals, finishPendingRequests }
}

describe('TaskWorkspace list request cancellation', () => {
  it.each(['empty', 'populated', 'failed'] as const)(
    'keeps the current %s list state when cancelled Task data arrives late',
    async (outcome) => {
      const { obsolete, current, signals, finishPendingRequests } = renderListFixture()
      const obsoleteTask = taskForTest('task-1', 'Task from the cancelled request')
      const currentTask = taskForTest('task-2', 'Task from the current request')
      function assertCurrentList() {
        if (outcome === 'empty') {
          expect(screen.getByText('暂无 Task')).toBeInTheDocument()
          expect(screen.queryByRole('list', { name: '最近 Task' })).not.toBeInTheDocument()
          expect(screen.queryByRole('alert')).not.toBeInTheDocument()
        } else if (outcome === 'populated') {
          expect(screen.getByRole('list', { name: '最近 Task' })).toBeInTheDocument()
          expect(screen.getByText(currentTask.goal)).toBeInTheDocument()
          expect(screen.getByText('task-2 · version 1')).toBeInTheDocument()
          expect(screen.getAllByRole('listitem')).toHaveLength(1)
          expect(screen.queryByRole('alert')).not.toBeInTheDocument()
        } else {
          expect(screen.getByRole('alert')).toHaveTextContent('列表加载失败：HTTP 503')
          expect(screen.queryByRole('list', { name: '最近 Task' })).not.toBeInTheDocument()
          expect(screen.queryByText('暂无 Task')).not.toBeInTheDocument()
        }
        expect(screen.queryByText('正在加载 Task…')).not.toBeInTheDocument()
      }
      try {
        expect(signals).toHaveLength(2)
        expect(signals[0]?.aborted).toBe(true)
        expect(signals[1]?.aborted).toBe(false)
        await act(async () => current.resolve(
          outcome === 'failed'
            ? Response.json({}, { status: 503 })
            : listResponse(outcome === 'empty' ? [] : [currentTask]),
        ))
        assertCurrentList()

        await act(async () => obsolete.resolve(listResponse([obsoleteTask])))
        expect(screen.queryByText(obsoleteTask.goal)).not.toBeInTheDocument()
        assertCurrentList()
      } finally {
        await finishPendingRequests()
      }
    },
  )

  it.each(['HTTP failure', 'network failure', 'AbortError'] as const)(
    'keeps loading the current list when a cancelled request returns %s',
    async (outcome) => {
      const { obsolete, current, signals, finishPendingRequests } = renderListFixture()
      const currentTask = taskForTest('task-2', 'Task from the current request')
      try {
        expect(signals).toHaveLength(2)
        expect(signals[0]?.aborted).toBe(true)
        expect(signals[1]?.aborted).toBe(false)
        await act(async () => {
          if (outcome === 'HTTP failure') {
            obsolete.resolve(Response.json({}, { status: 503 }))
          } else if (outcome === 'network failure') {
            obsolete.reject(new Error('obsolete network failure'))
          } else {
            obsolete.reject(new DOMException('obsolete request aborted', 'AbortError'))
          }
        })
        expect(screen.queryByRole('alert')).not.toBeInTheDocument()
        expect(screen.getByText('正在加载 Task…')).toBeInTheDocument()
        expect(screen.queryByText('暂无 Task')).not.toBeInTheDocument()
        expect(screen.queryByRole('list', { name: '最近 Task' })).not.toBeInTheDocument()

        await act(async () => current.resolve(listResponse([currentTask])))
        expect(screen.getByText(currentTask.goal)).toBeInTheDocument()
        expect(screen.getAllByRole('listitem')).toHaveLength(1)
        expect(screen.queryByRole('alert')).not.toBeInTheDocument()
        expect(screen.queryByText('正在加载 Task…')).not.toBeInTheDocument()
      } finally {
        await finishPendingRequests()
      }
    },
  )
})
