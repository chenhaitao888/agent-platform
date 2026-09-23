import {
  act,
  fireEvent,
  render,
  screen,
  within,
} from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskWorkspace from './TaskWorkspace'

afterEach(() => {
  vi.unstubAllGlobals()
})

const repositoryReference = {
  provider: 'gitlab',
  repositoryId: 'project-7',
  baseSha: '1111111111111111111111111111111111111111',
  headSha: '2222222222222222222222222222222222222222',
}

function fillValidTaskForm() {
  fireEvent.change(screen.getByLabelText('任务目标'), {
    target: { value: 'Review pull request 42' },
  })
  fireEvent.change(screen.getByLabelText('仓库 ID'), {
    target: { value: 'project-7' },
  })
  fireEvent.change(screen.getByLabelText('Base SHA'), {
    target: { value: repositoryReference.baseSha },
  })
  fireEvent.change(screen.getByLabelText('Head SHA'), {
    target: { value: repositoryReference.headSha },
  })
}

describe('TaskWorkspace', () => {
  it('loads and shows the most recent tasks', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [
              {
                id: 'task-2',
                type: 'BUG_FIX',
                goal: 'Fix checkout timeout',
                status: 'CREATED',
                createdAt: '2026-09-21T11:00:00Z',
              },
              {
                id: 'task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                status: 'CREATED',
                createdAt: '2026-09-21T10:00:00Z',
              },
            ],
          }),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    )

    render(<TaskWorkspace />)

    expect(screen.getByText('正在加载 Task…')).toBeInTheDocument()

    const list = await screen.findByRole('list', { name: '最近 Task' })
    const items = within(list).getAllByRole('listitem')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('Fix checkout timeout')
    expect(items[1]).toHaveTextContent('Review pull request 42')
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/tasks?tenantId=tenant-local',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(
      within(items[0]).getByRole('button', { name: '查看 task-2 的事件' }),
    ).toBeInTheDocument()
    expect(
      within(items[1]).getByRole('button', { name: '查看 task-1 的事件' }),
    ).toBeInTheDocument()
  })

  it('shows an empty state when no tasks exist', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    render(<TaskWorkspace />)

    expect(await screen.findByText('暂无 Task')).toBeInTheDocument()
  })

  it('shows an error when the task list cannot be loaded', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

    render(<TaskWorkspace />)

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '列表加载失败：network down',
    )
    expect(
      screen.getByRole('button', { name: '创建 Task' }),
    ).toBeEnabled()
  })

  it('adds a newly created task to the recent list', async () => {
    const fetchMock = vi.fn(
      (input: RequestInfo | URL, init?: RequestInit) => {
        if (input === '/api/v1/tasks' && init?.method === 'POST') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                repository: repositoryReference,
                status: 'CREATED',
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 201,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }

        return Promise.resolve(
          new Response(JSON.stringify({ items: [] }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        )
      },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskWorkspace />)
    await screen.findByText('暂无 Task')

    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    const list = await screen.findByRole('list', { name: '最近 Task' })
    expect(within(list).getByText('Review pull request 42')).toBeInTheDocument()
  })

  it('keeps a newly created task when an older list response arrives later', async () => {
    let resolveList: (response: Response) => void = () => undefined
    const pendingList = new Promise<Response>((resolve) => {
      resolveList = resolve
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        if (input === '/api/v1/tasks' && init?.method === 'POST') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                repository: repositoryReference,
                status: 'CREATED',
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 201,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }
        return pendingList
      }),
    )

    render(<TaskWorkspace />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    const list = await screen.findByRole('list', { name: '最近 Task' })
    expect(within(list).getByText('Review pull request 42')).toBeInTheDocument()

    resolveList(
      new Response(
        JSON.stringify({
          items: [
            {
              id: 'task-older',
              type: 'BUG_FIX',
              goal: 'Fix older task',
              status: 'CREATED',
              createdAt: '2026-09-21T09:00:00Z',
            },
          ],
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )

    await screen.findByText('Fix older task')
    expect(
      within(screen.getByRole('list', { name: '最近 Task' })).getByText(
        'Review pull request 42',
      ),
    ).toBeInTheDocument()
  })

  it('keeps a newly created task when the older list request fails later', async () => {
    let rejectList: (error: Error) => void = () => undefined
    const pendingList = new Promise<Response>((_resolve, reject) => {
      rejectList = reject
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        if (input === '/api/v1/tasks' && init?.method === 'POST') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                repository: repositoryReference,
                status: 'CREATED',
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 201,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }
        return pendingList
      }),
    )

    render(<TaskWorkspace />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    await screen.findByRole('list', { name: '最近 Task' })

    await act(async () => {
      rejectList(new Error('network down'))
      await pendingList.catch(() => undefined)
    })

    expect(
      within(screen.getByRole('list', { name: '最近 Task' })).getByText(
        'Review pull request 42',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('列表加载失败：network down')).not.toBeInTheDocument()
  })

  it('retries queueing with the same key after a lost response', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000010')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000011'),
    })
    let patchCalls = 0
    const fetchMock = vi.fn(
      (input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') {
          patchCalls++
          if (patchCalls === 1) {
            return Promise.reject(new Error('response lost'))
          }
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                tenantId: 'tenant-local',
                idempotencyKey: 'create-task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                status: 'QUEUED',
                version: 2,
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }

        return Promise.resolve(
          new Response(
            JSON.stringify({
              items: [
                {
                  id: 'task-1',
                  tenantId: 'tenant-local',
                  idempotencyKey: 'create-task-1',
                  type: 'PR_REVIEW',
                  goal: 'Review pull request 42',
                  status: 'CREATED',
                  version: 1,
                  createdAt: '2026-09-21T10:00:00Z',
                },
              ],
            }),
            {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            },
          ),
        )
      },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskWorkspace />)
    fireEvent.click(
      await screen.findByRole('button', { name: '将 task-1 加入队列' }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent('response lost')
    fireEvent.click(screen.getByRole('button', { name: '将 task-1 加入队列' }))

    expect(await screen.findByText('QUEUED')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: '将 task-1 加入队列' }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    ).toBeInTheDocument()
    const requests = fetchMock.mock.calls
      .filter(([, init]) => init?.method === 'PATCH')
      .map(([, init]) =>
        JSON.parse(String(init?.body)) as {
          requestId: string
          idempotencyKey: string
          expectedVersion: number
          status: string
        },
      )
    expect(requests).toHaveLength(2)
    expect(requests[0].requestId).not.toBe(requests[1].requestId)
    expect(requests[0].idempotencyKey).toBe('task-queue:v1:task-1:v1')
    expect(requests[1].idempotencyKey).toBe(requests[0].idempotencyKey)
    expect(requests[1].expectedVersion).toBe(1)
    expect(requests[1].status).toBe('QUEUED')
  })

  it('keeps the created task when queueing has a version conflict', async () => {
    const fetchMock = vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: 'version_conflict',
                message: 'task version does not match expectedVersion',
              }),
              {
                status: 409,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }

        return Promise.resolve(
          new Response(
            JSON.stringify({
              items: [
                {
                  id: 'task-1',
                  tenantId: 'tenant-local',
                  idempotencyKey: 'create-task-1',
                  type: 'PR_REVIEW',
                  goal: 'Review pull request 42',
                  status: 'CREATED',
                  version: 1,
                  createdAt: '2026-09-21T10:00:00Z',
                },
              ],
            }),
            {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            },
          ),
        )
      },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskWorkspace />)
    fireEvent.click(
      await screen.findByRole('button', { name: '将 task-1 加入队列' }),
    )

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Task 更新失败：task version does not match expectedVersion',
    )
    expect(screen.getByText('CREATED')).toBeInTheDocument()
    expect(screen.getByText('task-1 · version 1')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '将 task-1 加入队列' }),
    ).toBeEnabled()
  })

  it('does not replace a queued task with an older list snapshot', async () => {
    let resolveList: (response: Response) => void = () => undefined
    const pendingList = new Promise<Response>((resolve) => {
      resolveList = resolve
    })
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000020')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000021')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000022')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000023'),
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'POST') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                tenantId: 'tenant-local',
                idempotencyKey: 'create-task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                repository: repositoryReference,
                status: 'CREATED',
                version: 1,
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 201,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }
        if (init?.method === 'PATCH') {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                id: 'task-1',
                tenantId: 'tenant-local',
                idempotencyKey: 'create-task-1',
                type: 'PR_REVIEW',
                goal: 'Review pull request 42',
                status: 'QUEUED',
                version: 2,
                createdAt: '2026-09-21T10:00:00Z',
              }),
              {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
              },
            ),
          )
        }
        return pendingList
      }),
    )

    render(<TaskWorkspace />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    fireEvent.click(
      await screen.findByRole('button', { name: '将 task-1 加入队列' }),
    )
    await screen.findByText('task-1 · version 2')

    resolveList(
      new Response(
        JSON.stringify({
          items: [
            {
              id: 'task-1',
              tenantId: 'tenant-local',
              idempotencyKey: 'create-task-1',
              type: 'PR_REVIEW',
              goal: 'Review pull request 42',
              status: 'CREATED',
              version: 1,
              createdAt: '2026-09-21T10:00:00Z',
            },
            {
              id: 'task-older',
              tenantId: 'tenant-local',
              idempotencyKey: 'create-task-older',
              type: 'BUG_FIX',
              goal: 'Fix older task',
              status: 'QUEUED',
              version: 2,
              createdAt: '2026-09-21T09:00:00Z',
            },
          ],
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )

    await screen.findByText('Fix older task')
    const taskOne = screen.getByText(/task-1 · version/).closest('li')
    expect(taskOne).not.toBeNull()
    expect(within(taskOne as HTMLLIElement).getByText('QUEUED')).toBeInTheDocument()
    expect(within(taskOne as HTMLLIElement).getByText('task-1 · version 2')).toBeInTheDocument()
  })
})
