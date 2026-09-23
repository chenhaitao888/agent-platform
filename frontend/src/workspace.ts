export type Workspace = {
  id: string
  tenantId: string
  taskId: string
  repository: {
    provider: string
    repositoryId: string
  }
  targetBranch: 'master'
  targetSha: string
  baseSha: string
  headSha: string
  state: 'REGISTERED' | 'PREPARING' | 'READY'
  path?: string
  version: number
  createdAt: string
}
