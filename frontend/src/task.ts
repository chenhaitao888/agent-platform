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

export type TaskEvent = {
  schemaVersion: string
  eventId: string
  eventType: 'task.created' | 'task.queued'
  occurredAt: string
  tenantId: string
  taskId: string
  sequence: number
  correlationId: string
  causationId: string
  payload: {
    status: TaskStatus
    version: number
  }
}
