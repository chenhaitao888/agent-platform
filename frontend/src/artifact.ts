export type Artifact = {
  id: string
  tenantId: string
  taskId: string
  workspaceId: string
  type: 'REPOSITORY_DIFF'
  mediaType: 'text/x-diff'
  sha256: string
  sizeBytes: number
  createdAt: string
}
