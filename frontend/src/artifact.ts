export type Artifact = {
  id: string
  tenantId: string
  taskId: string
  workspaceId: string
  sha256: string
  sizeBytes: number
  createdAt: string
} & (
  | { type: 'REPOSITORY_DIFF'; mediaType: 'text/x-diff' }
  | { type: 'PR_REVIEW_FINDINGS'; mediaType: 'application/json' }
)
