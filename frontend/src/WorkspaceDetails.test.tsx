import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import WorkspaceDetails from './WorkspaceDetails'
import type { Task } from './task'

afterEach(() => {
  vi.unstubAllGlobals()
})

const queuedTask: Task = {
  id: 'task-1',
  tenantId: 'tenant-local',
  idempotencyKey: 'create-task-1',
  type: 'PR_REVIEW',
  goal: 'Review pull request 42',
  repository: {
    provider: 'gitlab',
    repositoryId: 'project-7',
    baseSha: '1111111111111111111111111111111111111111',
    headSha: '2222222222222222222222222222222222222222',
  },
  status: 'QUEUED',
  version: 2,
  createdAt: '2026-09-22T01:00:00Z',
}

describe('WorkspaceDetails', () => {
  it('loads and shows an existing workspace on request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          id: 'workspace-1',
          tenantId: 'tenant-local',
          taskId: 'task-1',
          repository: {
            provider: 'gitlab',
            repositoryId: 'project-7',
          },
          baseSha: '1111111111111111111111111111111111111111',
          headSha: '2222222222222222222222222222222222222222',
          state: 'REGISTERED',
          version: 1,
          createdAt: '2026-09-22T02:00:00Z',
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )

    const details = await screen.findByRole('group', {
      name: 'task-1 的 Workspace',
    })
    expect(details).toHaveTextContent('REGISTERED')
    expect(details).toHaveTextContent('gitlab / project-7')
    expect(details).toHaveTextContent(
      '1111111111111111111111111111111111111111',
    )
    expect(details).toHaveTextContent(
      '2222222222222222222222222222222222222222',
    )
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/tasks/task-1/workspace?tenantId=tenant-local',
    )
  })

  it('registers a workspace when the task does not have one', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000001')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000002'),
    })
    const registered = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: {
        provider: 'gitlab',
        repositoryId: 'project-7',
      },
      baseSha: '1111111111111111111111111111111111111111',
      headSha: '2222222222222222222222222222222222222222',
      state: 'REGISTERED',
      version: 1,
      createdAt: '2026-09-22T02:00:00Z',
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            error: 'not_found',
            message: 'workspace not found',
          }),
          {
            status: 404,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(registered), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )

    const repositoryReference = await screen.findByRole('group', {
      name: 'task-1 的仓库引用',
    })
    expect(repositoryReference).toHaveTextContent('gitlab / project-7')
    expect(repositoryReference).toHaveTextContent(
      '1111111111111111111111111111111111111111',
    )
    expect(repositoryReference).toHaveTextContent(
      '2222222222222222222222222222222222222222',
    )
    expect(screen.queryByLabelText('仓库 ID')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '登记 Workspace' }))

    const details = await screen.findByRole('group', {
      name: 'task-1 的 Workspace',
    })
    expect(details).toHaveTextContent('REGISTERED')
    expect(details).toHaveTextContent('gitlab / project-7')
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/tasks/task-1/workspace',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: 'req_00000000-0000-4000-8000-000000000001',
          idempotencyKey:
            'workspace-register:00000000-0000-4000-8000-000000000002',
          tenantId: 'tenant-local',
        }),
      },
    )
  })

  it('prepares a registered workspace and shows its real path', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000005')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000006'),
    })
    const registered = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: {
        provider: 'gitlab',
        repositoryId: 'project-7',
      },
      baseSha: '1111111111111111111111111111111111111111',
      headSha: '2222222222222222222222222222222222222222',
      state: 'REGISTERED',
      version: 1,
      createdAt: '2026-09-22T02:00:00Z',
    }
    const ready = {
      ...registered,
      state: 'READY',
      version: 3,
      path: '/var/lib/agent-platform/workspace-1/worktree',
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(registered), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(ready), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('REGISTERED')
    fireEvent.click(screen.getByRole('button', { name: '准备 Workspace' }))

    const details = await screen.findByRole('group', {
      name: 'task-1 的 Workspace',
    })
    expect(details).toHaveTextContent('READY')
    expect(details).toHaveTextContent(
      '/var/lib/agent-platform/workspace-1/worktree',
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/tasks/task-1/workspace/prepare',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: 'req_00000000-0000-4000-8000-000000000005',
          idempotencyKey:
            'workspace-prepare:00000000-0000-4000-8000-000000000006',
          tenantId: 'tenant-local',
          expectedVersion: 1,
        }),
      },
    )
  })

  it('shows a repository verification failure during registration', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000003')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000004'),
    })
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              error: 'not_found',
              message: 'workspace not found',
            }),
            {
              status: 404,
              headers: { 'Content-Type': 'application/json' },
            },
          ),
        )
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              error: 'repository_verification_unavailable',
              message: 'repository verification is unavailable',
            }),
            {
              status: 503,
              headers: { 'Content-Type': 'application/json' },
            },
          ),
        ),
    )

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByRole('group', { name: 'task-1 的仓库引用' })
    fireEvent.click(screen.getByRole('button', { name: '登记 Workspace' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Workspace 操作失败：repository verification is unavailable',
    )
  })
})
