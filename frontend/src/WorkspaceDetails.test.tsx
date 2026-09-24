import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    targetBranch: 'master',
    targetSha: '3333333333333333333333333333333333333333',
    baseSha: '1111111111111111111111111111111111111111',
    headSha: '2222222222222222222222222222222222222222',
  },
  status: 'QUEUED',
  version: 2,
  createdAt: '2026-09-22T01:00:00Z',
}

describe('WorkspaceDetails', () => {
  it('keeps the current Task diff when an earlier Task response arrives late', async () => {
    let finishOldDiff!: (response: Response) => void
    const oldDiff = new Promise<Response>((resolve) => {
      finishOldDiff = resolve
    })
    const ready = (taskId: string) => ({
      id: `workspace-${taskId}`,
      tenantId: 'tenant-local',
      taskId,
      repository: { provider: 'gitlab', repositoryId: 'project-7' },
      targetBranch: 'master',
      targetSha: queuedTask.repository.targetSha,
      baseSha: queuedTask.repository.baseSha,
      headSha: queuedTask.repository.headSha,
      state: 'READY',
      version: 3,
      path: '/tmp/worktree',
    })
    const fetchMock = vi.fn((url: string, _init?: RequestInit) => {
      if (url.includes('/task-1/workspace/diff')) return oldDiff
      if (url.includes('/task-2/workspace/diff')) {
        return Promise.resolve(new Response(
          JSON.stringify({
            mediaType: 'text/x-diff',
            sizeBytes: 9,
            sha256: 'new-sha',
            patch: 'NEW PATCH',
          }),
          { status: 200 },
        ))
      }
      if (url.includes('/task-1/workspace')) {
        return Promise.resolve(new Response(
          JSON.stringify(ready('task-1')),
          { status: 200 },
        ))
      }
      return Promise.resolve(new Response(
        JSON.stringify(ready('task-2')),
        { status: 200 },
      ))
    })
    vi.stubGlobal('fetch', fetchMock)

    const { rerender } = render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('READY')
    fireEvent.click(screen.getByRole('button', { name: '查看固定版本差异' }))

    rerender(<WorkspaceDetails task={{ ...queuedTask, id: 'task-2' }} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-2 的 Workspace' }),
    )
    await screen.findByText('READY')
    fireEvent.click(screen.getByRole('button', { name: '查看固定版本差异' }))
    expect(await screen.findByText('NEW PATCH')).toBeInTheDocument()

    await act(async () => {
      finishOldDiff(new Response(
        JSON.stringify({
          mediaType: 'text/x-diff',
          sizeBytes: 9,
          sha256: 'old-sha',
          patch: 'OLD PATCH',
        }),
        { status: 200 },
      ))
    })

    expect(screen.getByText('NEW PATCH')).toBeInTheDocument()
    expect(screen.queryByText('OLD PATCH')).not.toBeInTheDocument()
    const oldDiffCall = fetchMock.mock.calls.find(([url]) =>
      url.includes('/task-1/workspace/diff'),
    )
    expect(oldDiffCall?.[1]?.signal?.aborted).toBe(true)
  })

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
          targetBranch: 'master',
          targetSha: '3333333333333333333333333333333333333333',
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
    expect(details).toHaveTextContent('master')
    expect(details).toHaveTextContent(
      '3333333333333333333333333333333333333333',
    )
    expect(details).toHaveTextContent(
      '1111111111111111111111111111111111111111',
    )
    expect(details).toHaveTextContent(
      '2222222222222222222222222222222222222222',
    )
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/tasks/task-1/workspace?tenantId=tenant-local',
      { signal: expect.any(AbortSignal) },
    )
  })

  it('reuses the registration key after a lost response and loading the workspace again', async () => {
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
      targetBranch: queuedTask.repository.targetBranch,
      targetSha: queuedTask.repository.targetSha,
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
      .mockRejectedValueOnce(new Error('response lost'))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ error: 'not_found', message: 'workspace not found' }),
          { status: 404, headers: { 'Content-Type': 'application/json' } },
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
    expect(repositoryReference).toHaveTextContent('master')
    expect(repositoryReference).toHaveTextContent(
      '3333333333333333333333333333333333333333',
    )
    expect(repositoryReference).toHaveTextContent(
      '1111111111111111111111111111111111111111',
    )
    expect(repositoryReference).toHaveTextContent(
      '2222222222222222222222222222222222222222',
    )
    expect(screen.queryByLabelText('仓库 ID')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '登记 Workspace' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('response lost')
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByRole('group', { name: 'task-1 的仓库引用' })
    fireEvent.click(screen.getByRole('button', { name: '登记 Workspace' }))

    const details = await screen.findByRole('group', {
      name: 'task-1 的 Workspace',
    })
    expect(details).toHaveTextContent('REGISTERED')
    expect(details).toHaveTextContent('gitlab / project-7')
    const requests = [1, 3].map((index) =>
      JSON.parse(String(fetchMock.mock.calls[index][1]?.body)) as {
        requestId: string
        idempotencyKey: string
        tenantId: string
      },
    )
    expect(requests[0].requestId).not.toBe(requests[1].requestId)
    expect(requests[0].idempotencyKey).toBe('workspace-register:v1:task-1')
    expect(requests[1].idempotencyKey).toBe(requests[0].idempotencyKey)
    expect(requests[1].tenantId).toBe('tenant-local')
  })

  it('reuses the preparation key after a lost response and loading the workspace again', async () => {
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
      targetBranch: queuedTask.repository.targetBranch,
      targetSha: queuedTask.repository.targetSha,
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
      .mockRejectedValueOnce(new Error('response lost'))
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
    expect(await screen.findByRole('alert')).toHaveTextContent('response lost')
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
    const requests = [1, 3].map((index) =>
      JSON.parse(String(fetchMock.mock.calls[index][1]?.body)) as {
        requestId: string
        idempotencyKey: string
        expectedVersion: number
      },
    )
    expect(requests[0].requestId).not.toBe(requests[1].requestId)
    expect(requests[0].idempotencyKey).toBe('workspace-prepare:v1:task-1:v1')
    expect(requests[1].idempotencyKey).toBe(requests[0].idempotencyKey)
    expect(requests[1].expectedVersion).toBe(1)
  })

  it('uses a new preparation key after the workspace version advances on failure', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('request-1')
        .mockReturnValueOnce('request-2'),
    })
    const registered = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: { provider: 'gitlab', repositoryId: 'project-7' },
      targetBranch: queuedTask.repository.targetBranch,
      targetSha: queuedTask.repository.targetSha,
      baseSha: queuedTask.repository.baseSha,
      headSha: queuedTask.repository.headSha,
      state: 'REGISTERED',
      version: 1,
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(registered), { status: 200 }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            error: 'workspace_preparation_failed',
            message: 'workspace preparation failed',
          }),
          { status: 503 },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ...registered, version: 3 }), {
          status: 200,
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            ...registered,
            state: 'READY',
            version: 5,
            path: '/tmp/worktree',
          }),
          { status: 200 },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('REGISTERED')
    fireEvent.click(screen.getByRole('button', { name: '准备 Workspace' }))
    await screen.findByRole('alert')
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('REGISTERED')
    fireEvent.click(screen.getByRole('button', { name: '准备 Workspace' }))
    await screen.findByText('READY')

    const requests = [1, 3].map((index) =>
      JSON.parse(String(fetchMock.mock.calls[index][1]?.body)) as {
        idempotencyKey: string
        expectedVersion: number
      },
    )
    expect(requests[0]).toMatchObject({
      idempotencyKey: 'workspace-prepare:v1:task-1:v1',
      expectedVersion: 1,
    })
    expect(requests[1]).toMatchObject({
      idempotencyKey: 'workspace-prepare:v1:task-1:v3',
      expectedVersion: 3,
    })
  })

  it('loads the immutable diff for a ready workspace', async () => {
    const ready = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: {
        provider: 'gitlab',
        repositoryId: 'project-7',
      },
      targetBranch: queuedTask.repository.targetBranch,
      targetSha: queuedTask.repository.targetSha,
      baseSha: '1111111111111111111111111111111111111111',
      headSha: '2222222222222222222222222222222222222222',
      state: 'READY',
      version: 3,
      path: '/var/lib/agent-platform/workspace-1/worktree',
      createdAt: '2026-09-22T02:00:00Z',
    }
    const patch = 'diff --git a/README.md b/README.md\n-base\n+head\n'
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(ready), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            taskId: 'task-1',
            workspaceId: 'workspace-1',
            baseSha: ready.baseSha,
            headSha: ready.headSha,
            mediaType: 'text/x-diff',
            sha256:
              'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
            sizeBytes: patch.length,
            patch,
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
    await screen.findByText('READY')
    fireEvent.click(
      screen.getByRole('button', { name: '查看固定版本差异' }),
    )

    const diff = await screen.findByRole('region', {
      name: 'task-1 的固定版本差异',
    })
    expect(diff).toHaveTextContent('text/x-diff')
    expect(diff).toHaveTextContent(`${patch.length} bytes`)
    expect(diff).toHaveTextContent('-base')
    expect(diff).toHaveTextContent('+head')
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-local',
      { signal: expect.any(AbortSignal) },
    )
  })

  it('bounds a large diff preview without changing its reported full size', async () => {
    const patch = 'A'.repeat(70_000) + 'TAIL'
    const ready = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: { provider: 'gitlab', repositoryId: 'project-7' },
      targetBranch: 'master',
      targetSha: queuedTask.repository.targetSha,
      baseSha: queuedTask.repository.baseSha,
      headSha: queuedTask.repository.headSha,
      state: 'READY',
      version: 3,
    }
    vi.stubGlobal(
      'fetch',
      vi.fn()
        .mockResolvedValueOnce(
          new Response(JSON.stringify(ready), { status: 200 }),
        )
        .mockResolvedValueOnce(new Response(
          JSON.stringify({
            mediaType: 'text/x-diff',
            sizeBytes: patch.length,
            sha256: 'full-patch-sha',
            patch,
          }),
          { status: 200 },
        )),
    )

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('READY')
    fireEvent.click(screen.getByRole('button', { name: '查看固定版本差异' }))

    const diff = await screen.findByRole('region', {
      name: 'task-1 的固定版本差异',
    })
    expect(diff.querySelector('pre')?.textContent?.length).toBeLessThanOrEqual(
      65_536,
    )
    expect(diff).not.toHaveTextContent('TAIL')
    expect(diff).toHaveTextContent(`${patch.length} bytes`)
    expect(diff).toHaveTextContent('仅预览前 65,536 个字符')
  })

  it('archives a loaded diff and shows the Artifact link', async () => {
    vi.stubGlobal('crypto', {
      randomUUID: vi
        .fn()
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000007')
        .mockReturnValueOnce('00000000-0000-4000-8000-000000000008'),
    })
    const ready = {
      id: 'workspace-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      repository: {
        provider: 'gitlab',
        repositoryId: 'project-7',
      },
      targetBranch: queuedTask.repository.targetBranch,
      targetSha: queuedTask.repository.targetSha,
      baseSha: '1111111111111111111111111111111111111111',
      headSha: '2222222222222222222222222222222222222222',
      state: 'READY',
      version: 3,
      path: '/var/lib/agent-platform/workspace-1/worktree',
      createdAt: '2026-09-22T02:00:00Z',
    }
    const patch = 'diff --git a/README.md b/README.md\n-base\n+head\n'
    const diff = {
      taskId: 'task-1',
      workspaceId: 'workspace-1',
      baseSha: ready.baseSha,
      headSha: ready.headSha,
      mediaType: 'text/x-diff',
      sha256:
        'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      sizeBytes: patch.length,
      patch,
    }
    const artifact = {
      id: 'artifact-1',
      tenantId: 'tenant-local',
      taskId: 'task-1',
      workspaceId: 'workspace-1',
      type: 'REPOSITORY_DIFF',
      mediaType: 'text/x-diff',
      sha256: diff.sha256,
      sizeBytes: patch.length,
      createdAt: '2026-09-22T03:00:00Z',
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(ready), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(diff), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(artifact), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(artifact), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    render(<WorkspaceDetails task={queuedTask} />)
    fireEvent.click(
      screen.getByRole('button', { name: '查看 task-1 的 Workspace' }),
    )
    await screen.findByText('READY')
    fireEvent.click(
      screen.getByRole('button', { name: '查看固定版本差异' }),
    )
    await screen.findByRole('region', { name: 'task-1 的固定版本差异' })
    fireEvent.click(screen.getByRole('button', { name: '归档为 Artifact' }))

    const archived = await screen.findByRole('region', {
      name: 'task-1 的 Diff Artifact',
    })
    expect(archived).toHaveTextContent('artifact-1')
    expect(archived).toHaveTextContent('REPOSITORY_DIFF')
    expect(
      screen.getByRole('link', { name: '读取 Artifact 内容' }),
    ).toHaveAttribute(
      'href',
      '/api/v1/artifacts/artifact-1/content?tenantId=tenant-local',
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/tasks/task-1/artifacts/diff',
      {
        method: 'POST',
        signal: expect.any(AbortSignal),
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: 'req_00000000-0000-4000-8000-000000000007',
          idempotencyKey: 'artifact-diff:v1:task-1:workspace-1:v3',
          tenantId: 'tenant-local',
          expectedWorkspaceVersion: 3,
        }),
      },
    )

    fireEvent.click(screen.getByRole('button', { name: '归档为 Artifact' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4))
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/tasks/task-1/artifacts/diff',
      {
        method: 'POST',
        signal: expect.any(AbortSignal),
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          requestId: 'req_00000000-0000-4000-8000-000000000008',
          idempotencyKey: 'artifact-diff:v1:task-1:workspace-1:v3',
          tenantId: 'tenant-local',
          expectedWorkspaceVersion: 3,
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
