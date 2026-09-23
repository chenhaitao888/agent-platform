import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import TaskEventTimeline from './TaskEventTimeline'
import type { Task } from './task'

afterEach(() => {
  vi.unstubAllGlobals()
})

const task: Task = {
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

describe('TaskEventTimeline', () => {
  it('loads and shows a task event timeline on request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            {
              schemaVersion: '2.0',
              eventId: 'evt-1',
              eventType: 'task.created',
              occurredAt: '2026-09-22T01:00:00Z',
              tenantId: 'tenant-local',
              taskId: 'task-1',
              sequence: 1,
              correlationId: 'task-1',
              causationId: 'req-create-task-1',
              payload: { task: { status: 'CREATED', version: 1 } },
            },
            {
              schemaVersion: '2.0',
              eventId: 'evt-2',
              eventType: 'task.queued',
              occurredAt: '2026-09-22T01:01:00Z',
              tenantId: 'tenant-local',
              taskId: 'task-1',
              sequence: 2,
              correlationId: 'task-1',
              causationId: 'req-queue-task-1',
              payload: { task: { status: 'QUEUED', version: 2 } },
            },
            {
              schemaVersion: '2.0',
              eventId: 'evt-3',
              eventType: 'workspace.registered',
              occurredAt: '2026-09-22T01:02:00Z',
              tenantId: 'tenant-local',
              taskId: 'task-1',
              sequence: 3,
              correlationId: 'task-1',
              causationId: 'req-register-workspace-1',
              payload: {
                workspace: {
                  workspaceId: 'workspace-1',
                  state: 'REGISTERED',
                  version: 1,
                },
              },
            },
            {
              schemaVersion: '2.0',
              eventId: 'evt-4',
              eventType: 'artifact.created',
              occurredAt: '2026-09-22T01:03:00Z',
              tenantId: 'tenant-local',
              taskId: 'task-1',
              sequence: 4,
              correlationId: 'task-1',
              causationId: 'req-archive-diff-1',
              payload: {
                artifact: {
                  artifactId: 'artifact-1',
                  workspaceId: 'workspace-1',
                  type: 'REPOSITORY_DIFF',
                  mediaType: 'text/x-diff',
                  sha256: 'abc123',
                  sizeBytes: 42,
                },
              },
            },
          ],
        }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<TaskEventTimeline task={task} />)
    fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))

    const timeline = await screen.findByRole('list', {
      name: 'task-1 的事件时间线',
    })
    expect(timeline).toHaveTextContent('task.created')
    expect(timeline).toHaveTextContent('CREATED · version 1')
    expect(timeline).toHaveTextContent('task.queued')
    expect(timeline).toHaveTextContent('QUEUED · version 2')
    expect(timeline).toHaveTextContent('workspace.registered')
    expect(timeline).toHaveTextContent('REGISTERED · version 1 · workspace-1')
    expect(timeline).toHaveTextContent('artifact.created')
    expect(timeline).toHaveTextContent('artifact-1 · REPOSITORY_DIFF · 42 B')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/tasks/task-1/events?tenantId=tenant-local',
    )
  })

  it('shows an error and allows retrying when events cannot be loaded', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: 'not_found',
            message: 'task not found',
          }),
          {
            status: 404,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    )

    render(<TaskEventTimeline task={task} />)
    fireEvent.click(screen.getByRole('button', { name: '查看 task-1 的事件' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '事件加载失败：task not found',
    )
    expect(
      screen.getByRole('button', { name: '查看 task-1 的事件' }),
    ).toBeEnabled()
  })
})
