import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskCreator from './TaskCreator'

afterEach(() => {
  vi.unstubAllGlobals()
})

function fillValidTaskForm() {
  fireEvent.change(screen.getByLabelText('任务目标'), {
    target: { value: 'Review pull request 42' },
  })
  fireEvent.change(screen.getByLabelText('仓库 ID'), {
    target: { value: 'platform/project-7' },
  })
  fireEvent.change(screen.getByLabelText('Head SHA'), {
    target: { value: '2222222222222222222222222222222222222222' },
  })
}

describe('TaskCreator', () => {
  it('explains that the platform derives the base from master', () => {
    render(<TaskCreator />)

    expect(screen.getByText(/master.*平台计算/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Base SHA')).not.toBeInTheDocument()
  })

  it('creates a task and shows the server result', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000001')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000002'),
    })
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          id: 'task-1',
          tenantId: 'tenant-local',
          idempotencyKey:
            'task-create:00000000-0000-4000-8000-000000000002',
          type: 'PR_REVIEW',
          goal: 'Review pull request 42',
          repository: {
            provider: 'gitlab',
            repositoryId: 'platform/project-7',
            targetBranch: 'master',
            targetSha: '3333333333333333333333333333333333333333',
            baseSha: '1111111111111111111111111111111111111111',
            headSha: '2222222222222222222222222222222222222222',
          },
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
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(await screen.findByText('task-1')).toBeInTheDocument()
    expect(screen.getByText('CREATED')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/tasks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        requestId: 'req_00000000-0000-4000-8000-000000000001',
        idempotencyKey:
          'task-create:00000000-0000-4000-8000-000000000002',
        tenantId: 'tenant-local',
        type: 'PR_REVIEW',
        goal: 'Review pull request 42',
        repository: {
          provider: 'gitlab',
          repositoryId: 'platform/project-7',
          headSha: '2222222222222222222222222222222222222222',
        },
      }),
    })
  })

  it('prevents duplicate submissions while creation is in progress', () => {
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => {})))

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(screen.getByRole('button', { name: '创建中…' })).toBeDisabled()
  })

  it('shows the backend message when task creation is rejected', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: 'validation_error',
            message: 'type and goal are required',
          }),
          {
            status: 400,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    )

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '创建失败：type and goal are required',
    )
    expect(screen.getByRole('button', { name: '创建 Task' })).toBeEnabled()
  })

  it('recovers when the backend cannot be reached', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '创建失败：network down',
    )
    expect(screen.getByRole('button', { name: '创建 Task' })).toBeEnabled()
  })

  it('reuses the creation key when the same form is retried after a network error', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000001')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000002')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000003'),
    })
    const fetchMock = vi
      .fn((_input: RequestInfo | URL, _init?: RequestInit) =>
        Promise.resolve(new Response()),
      )
      .mockRejectedValueOnce(new Error('response lost'))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            id: 'task-1',
            tenantId: 'tenant-local',
            type: 'PR_REVIEW',
            goal: 'Review pull request 42',
            repository: { provider: 'gitlab', repositoryId: 'platform/project-7' },
            status: 'CREATED',
            version: 1,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('response lost')
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    expect(await screen.findByText('task-1')).toBeInTheDocument()

    const requests = fetchMock.mock.calls.map(([, init]) =>
      JSON.parse(String(init?.body)) as {
        requestId: string
        idempotencyKey: string
      },
    )
    expect(requests).toHaveLength(2)
    expect(requests[0].requestId).not.toBe(requests[1].requestId)
    expect(requests[0].idempotencyKey).toBe(requests[1].idempotencyKey)
  })

  it('starts a new creation operation when the form content changes', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('request-1')
        .mockReturnValueOnce('key-1')
        .mockReturnValueOnce('request-2')
        .mockReturnValueOnce('key-2'),
    })
    const fetchMock = vi.fn(
      (_input: RequestInfo | URL, _init?: RequestInit) =>
        Promise.reject(new Error('response lost')),
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    await screen.findByRole('alert')
    fireEvent.change(screen.getByLabelText('任务目标'), {
      target: { value: 'Review pull request 43' },
    })
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

    const requests = fetchMock.mock.calls.map(([, init]) =>
      JSON.parse(String(init?.body)) as { goal: string; idempotencyKey: string },
    )
    expect(requests[0].goal).toBe('Review pull request 42')
    expect(requests[1].goal).toBe('Review pull request 43')
    expect(requests[0].idempotencyKey).not.toBe(requests[1].idempotencyKey)
  })

  it('uses a new key for another submission after a successful creation', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('request-1')
        .mockReturnValueOnce('key-1')
        .mockReturnValueOnce('request-2')
        .mockReturnValueOnce('key-2'),
    })
    let nextTask = 1
    const fetchMock = vi.fn(
      (_input: RequestInfo | URL, _init?: RequestInit) =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              id: `task-${nextTask++}`,
              goal: 'Review pull request 42',
              repository: { provider: 'gitlab', repositoryId: 'platform/project-7' },
              status: 'CREATED',
            }),
            { status: 201 },
          ),
        ),
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskCreator />)
    fillValidTaskForm()
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    await screen.findByText('task-1')
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))
    await screen.findByText('task-2')

    const keys = fetchMock.mock.calls.map(([, init]) =>
      (JSON.parse(String(init?.body)) as { idempotencyKey: string })
        .idempotencyKey,
    )
    expect(keys).toHaveLength(2)
    expect(keys[0]).not.toBe(keys[1])
  })
})
