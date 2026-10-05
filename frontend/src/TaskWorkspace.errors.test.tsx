import { act, fireEvent, render, screen, within } from '@testing-library/react'
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

const conflictMessage = 'task version does not match expectedVersion'
const networkMessage = 'task-2 response lost'

function conflictResponse() {
  return Response.json(
    { error: 'version_conflict', message: conflictMessage },
    { status: 409 },
  )
}

function queuedResponse(task: Task) {
  return Response.json({ ...task, status: 'QUEUED', version: 2 })
}

async function renderQueueFixture() {
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
  const pending: ReturnType<typeof deferredResponse>[][] = tasks.map(() => [])
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === 'PATCH') {
      const index = tasks.findIndex(
        (task) => input === `/api/v1/tasks/${task.id}`,
      )
      if (index >= 0) {
        const request = deferredResponse()
        pending[index].push(request)
        return request.response
      }
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
  async function finishPendingRequests() {
    await act(async () => {
      pending.forEach((requests, index) => {
        requests.forEach((request) => request.resolve(queuedResponse(tasks[index])))
      })
    })
  }
  return { tasks, pending, cards, buttons, finishPendingRequests }
}

describe('TaskWorkspace queue errors', () => {
  it('preserves other Task errors and clears only the retried Task error', async () => {
    const { tasks, pending, cards, buttons, finishPendingRequests } =
      await renderQueueFixture()
    try {
      fireEvent.click(buttons[0])
      await act(async () => pending[0][0].resolve(conflictResponse()))
      expect(screen.getByRole('alert')).toHaveTextContent(conflictMessage)

      fireEvent.click(buttons[1])
      expect(screen.getByRole('alert')).toHaveTextContent(conflictMessage)
      expect(within(cards[0]).getByRole('alert')).toHaveTextContent(conflictMessage)
      expect(within(cards[1]).queryByRole('alert')).not.toBeInTheDocument()
      expect(buttons[0]).toBeEnabled()
      expect(buttons[1]).toBeDisabled()

      await act(async () => pending[1][0].reject(new Error(networkMessage)))
      expect(within(cards[0]).getByRole('alert')).toHaveTextContent(conflictMessage)
      expect(within(cards[1]).getByRole('alert')).toHaveTextContent(networkMessage)
      expect(screen.getAllByRole('alert')).toHaveLength(2)

      fireEvent.click(buttons[0])
      expect(within(cards[0]).queryByRole('alert')).not.toBeInTheDocument()
      expect(within(cards[1]).getByRole('alert')).toHaveTextContent(networkMessage)
      expect(buttons[0]).toBeDisabled()
      expect(buttons[1]).toBeEnabled()
      await act(async () => pending[0][1].resolve(queuedResponse(tasks[0])))
      expect(within(cards[0]).getByText('task-1 · version 2')).toBeInTheDocument()
      expect(within(cards[1]).getByText('task-2 · version 1')).toBeInTheDocument()
      expect(within(cards[1]).getByRole('alert')).toHaveTextContent(networkMessage)

      fireEvent.click(buttons[1])
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      await act(async () => pending[1][1].resolve(queuedResponse(tasks[1])))
      expect(within(cards[1]).getByText('task-2 · version 2')).toBeInTheDocument()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(screen.getAllByText('QUEUED')).toHaveLength(2)
      expect(pending.map((requests) => requests.length)).toEqual([2, 2])
    } finally {
      await finishPendingRequests()
    }
  })

  it.each([
    { name: 'server rejection arrives first', firstToFinish: 0 },
    { name: 'network failure arrives first', firstToFinish: 1 },
  ])('keeps both errors on their Task cards when $name', async ({ firstToFinish }) => {
    const { pending, cards, buttons, finishPendingRequests } =
      await renderQueueFixture()
    const messages = [conflictMessage, networkMessage]
    async function failRequest(index: number) {
      await act(async () => {
        if (index === 0) pending[0][0].resolve(conflictResponse())
        else pending[1][0].reject(new Error(networkMessage))
      })
    }
    try {
      fireEvent.click(buttons[0])
      fireEvent.click(buttons[1])
      expect(buttons[0]).toBeDisabled()
      expect(buttons[1]).toBeDisabled()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()

      const stillPending = 1 - firstToFinish
      await failRequest(firstToFinish)
      expect(within(cards[firstToFinish]).getByRole('alert')).toHaveTextContent(
        messages[firstToFinish],
      )
      expect(within(cards[stillPending]).queryByRole('alert')).not.toBeInTheDocument()
      expect(buttons[firstToFinish]).toBeEnabled()
      expect(buttons[stillPending]).toBeDisabled()

      await failRequest(stillPending)
      expect(screen.getAllByRole('alert')).toHaveLength(2)
      for (const [index, card] of cards.entries()) {
        const alert = within(card).getByRole('alert')
        expect(alert).toHaveTextContent(messages[index])
        expect(alert).not.toHaveTextContent(messages[1 - index])
        expect(within(card).getByText('CREATED')).toBeInTheDocument()
        expect(within(card).getByText(`task-${index + 1} · version 1`)).toBeInTheDocument()
        expect(buttons[index]).toBeEnabled()
      }
    } finally {
      await finishPendingRequests()
    }
  })
})
