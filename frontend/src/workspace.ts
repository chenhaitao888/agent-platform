export type Workspace = {
  id: string
  tenantId: string
  taskId: string
  repository: {
    provider: string
    repositoryId: string
  }
  baseSha: string
  headSha: string
  state: 'REGISTERED' | 'PREPARING' | 'READY'
  path?: string
  version: number
  createdAt: string
}
