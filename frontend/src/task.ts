export const LOCAL_TENANT_ID = 'tenant-local'

export type TaskStatus = 'CREATED' | 'QUEUED'

export type RepositoryReference = {
  provider: 'gitlab'
  repositoryId: string
  baseSha: string
  headSha: string
}

export type Task = {
  id: string
  tenantId: string
  idempotencyKey: string
  type: string
  goal: string
  repository: RepositoryReference
  status: TaskStatus
  version: number
  createdAt: string
}

type TaskEventEnvelope = {
  schemaVersion: '2.0'
  eventId: string
  occurredAt: string
  tenantId: string
  taskId: string
  sequence: number
  correlationId: string
  causationId: string
}

// eventType 决定 payload 的形状；Workspace 的 version 不能误读成 Task 的 version。
export type TaskEvent = TaskEventEnvelope &
  (
    | {
        eventType: 'task.created' | 'task.queued'
        payload: { task: { status: TaskStatus; version: number } }
      }
    | {
        eventType:
          | 'workspace.registered'
          | 'workspace.preparing'
          | 'workspace.ready'
          | 'workspace.preparation_failed'
        payload: {
          workspace: {
            workspaceId: string
            state: 'REGISTERED' | 'PREPARING' | 'READY'
            version: number
          }
        }
      }
    | {
        eventType: 'artifact.created'
        payload: {
          artifact: {
            artifactId: string
            workspaceId: string
            type: string
            mediaType: string
            sha256: string
            sizeBytes: number
          }
        }
      }
  )
