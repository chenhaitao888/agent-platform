import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskWorkspace from './TaskWorkspace'
import type { Task } from './task'

afterEach(() => {
  vi.unstubAllGlobals()
})

function pendingResponse() {
  let resolve: (response: Response) => void = () => undefined
  const response = new Promise<Response>((complete) => {
    resolve = complete
  })
  return { response, resolve }
}

function queuedResponse(task: Task) {
  return Response.json({ ...task, status: 'QUEUED', version: 2 })
}

describe('TaskWorkspace concurrent queueing', () => {
  it.each([
    { name: 'task-1 succeeds first', firstToFinish: 0, outcome: 'success' },
    { name: 'task-2 succeeds first', firstToFinish: 1, outcome: 'success' },
    { name: 'task-1 is rejected first', firstToFinish: 0, outcome: 'rejected' },
    { name: 'task-2 is rejected first', firstToFinish: 1, outcome: 'rejected' },
  ])('keeps queue buttons independent when $name', async ({ firstToFinish, outcome }) => {
    const tasks: Task[] = [1, 2].map((number) => ({
      id: `task-${number}`,
      tenantId: 'tenant-local',
      idempotencyKey: `create-task-${number}`,
      type: 'PR_REVIEW',
      goal: `Review task ${number}`,
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
    }))
    const pending = tasks.map(() => pendingResponse())
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        const index = tasks.findIndex(
          (task) => input === `/api/v1/tasks/${task.id}`,
        )
        if (index >= 0) return pending[index].response
      }
      if (input === '/api/v1/tasks?tenantId=tenant-local') {
        return Promise.resolve(Response.json({ items: tasks }))
      }
      return Promise.reject(new Error(`Unexpected request: ${String(input)}`))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskWorkspace />)
    const list = await screen.findByRole('list', { name: '最近 Task' })
    const cards = within(list).getAllByRole('listitem')
    const buttons = tasks.map((task) =>
      screen.getByRole('button', { name: `将 ${task.id} 加入队列` }),
    )
    const patchCalls = () =>
      fetchMock.mock.calls.filter(([, init]) => init?.method === 'PATCH')

    try {
      fireEvent.click(buttons[0])
      expect(buttons[0]).toBeDisabled()
      expect(buttons[1]).toBeEnabled()
      fireEvent.click(buttons[1])
      expect(buttons[0]).toBeDisabled()
      expect(buttons[1]).toBeDisabled()
      fireEvent.click(buttons[0])
      fireEvent.click(buttons[1])
      expect(patchCalls()).toHaveLength(2)

      for (const [index, [input, init]] of patchCalls().entries()) {
        expect(input).toBe(`/api/v1/tasks/${tasks[index].id}`)
        expect(JSON.parse(String(init?.body))).toEqual(
          expect.objectContaining({
            tenantId: 'tenant-local',
            idempotencyKey: `task-queue:v1:${tasks[index].id}:v1`,
            expectedVersion: 1,
            status: 'QUEUED',
          }),
        )
      }

      const stillPending = 1 - firstToFinish
      await act(async () => pending[firstToFinish].resolve(
        outcome === 'success'
          ? queuedResponse(tasks[firstToFinish])
          : Response.json({
              error: 'version_conflict',
              message: 'task version does not match expectedVersion',
            }, { status: 409 }),
      ))
      if (outcome === 'success') {
        expect(within(cards[firstToFinish]).getByText('QUEUED')).toBeInTheDocument()
        expect(within(cards[firstToFinish]).getByText(
          `${tasks[firstToFinish].id} · version 2`,
        )).toBeInTheDocument()
        expect(screen.queryByRole('button', {
          name: `将 ${tasks[firstToFinish].id} 加入队列`,
        })).not.toBeInTheDocument()
      } else {
        expect(within(cards[firstToFinish]).getByText('CREATED')).toBeInTheDocument()
        expect(within(cards[firstToFinish]).getByText(
          `${tasks[firstToFinish].id} · version 1`,
        )).toBeInTheDocument()
        expect(buttons[firstToFinish]).toBeEnabled()
        expect(screen.getByRole('alert')).toHaveTextContent(
          'Task 更新失败：task version does not match expectedVersion',
        )
      }
      expect(buttons[stillPending]).toBeDisabled()
      expect(within(cards[stillPending]).getByText('CREATED')).toBeInTheDocument()
      expect(within(cards[stillPending]).getByText(
        `${tasks[stillPending].id} · version 1`,
      )).toBeInTheDocument()
      fireEvent.click(buttons[stillPending])
      expect(patchCalls()).toHaveLength(2)

      await act(async () => pending[stillPending].resolve(queuedResponse(tasks[stillPending])))
      expect(within(cards[stillPending]).getByText('QUEUED')).toBeInTheDocument()
      expect(within(cards[stillPending]).getByText(
        `${tasks[stillPending].id} · version 2`,
      )).toBeInTheDocument()
      expect(screen.queryByRole('button', {
        name: `将 ${tasks[stillPending].id} 加入队列`,
      })).not.toBeInTheDocument()
      if (outcome === 'rejected') expect(buttons[firstToFinish]).toBeEnabled()
    } finally {
      await act(async () => {
        pending.forEach((request, index) =>
          request.resolve(queuedResponse(tasks[index])),
        )
      })
    }
  })
})
