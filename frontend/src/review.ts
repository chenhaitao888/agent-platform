export type WorkspaceDiff = {
  taskId: string
  workspaceId: string
  baseSha: string
  headSha: string
  mediaType: 'text/x-diff'
  sha256: string
  sizeBytes: number
  patch: string
}
