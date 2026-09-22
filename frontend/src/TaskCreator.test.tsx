import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskCreator from './TaskCreator'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('TaskCreator', () => {
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
    fireEvent.change(screen.getByLabelText('任务目标'), {
      target: { value: 'Review pull request 42' },
    })
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
      }),
    })
  })

  it('prevents duplicate submissions while creation is in progress', () => {
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => {})))

    render(<TaskCreator />)
    fireEvent.change(screen.getByLabelText('任务目标'), {
      target: { value: 'Review pull request 42' },
    })
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
    fireEvent.change(screen.getByLabelText('任务目标'), {
      target: { value: 'Review pull request 42' },
    })
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '创建失败：type and goal are required',
    )
    expect(screen.getByRole('button', { name: '创建 Task' })).toBeEnabled()
  })

  it('recovers when the backend cannot be reached', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

    render(<TaskCreator />)
    fireEvent.change(screen.getByLabelText('任务目标'), {
      target: { value: 'Review pull request 42' },
    })
    fireEvent.click(screen.getByRole('button', { name: '创建 Task' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '创建失败：network down',
    )
    expect(screen.getByRole('button', { name: '创建 Task' })).toBeEnabled()
  })
})
