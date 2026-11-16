export type ServiceEntry = {
  serviceId: string
  name: string
  catalogVersion: string
  defaultTargetBranch: string
  allowedTargetBranches: string[]
  environments: string[]
}
export type ServiceSelection = { serviceId: string; sourceBranch: string; targetBranch: string }
export type ServiceSnapshot = ServiceSelection & {
  catalogVersion: string
  resolvedAt: string
  errorCode?: string
  repository?: { repositoryId: string; headSha: string; targetSha: string; baseSha: string; targetBranch: string }
}
export type ChangeSnapshot = {
  id: string
  revision: number
  state: 'READY' | 'FAILED'
  inputHash: string
  createdAt: string
  services: ServiceSnapshot[]
}
export type ChangeRequest = {
  id: string
  tenantId: string
  title: string
  description: string
  environment: string
  services: ServiceSelection[]
  version: number
  currentSnapshotId?: string
  snapshots: ChangeSnapshot[]
  createdAt: string
}
export type SnapshotResult = { changeId: string; changeVersion: number; snapshot: ChangeSnapshot }
export const changeErrors: Record<string, string> = {
  source_branch_not_found: '源分支不存在或不可访问',
  target_branch_not_found: '目标分支不存在或不可访问',
  repository_not_found: '仓库不存在或不可访问',
  review_base_not_found: '源分支和目标分支没有可用的共同祖先',
  repository_resolution_unavailable: '暂时无法解析仓库，请稍后重试',
  resolution_canceled: '解析已中止，需要重新固定快照',
  service_forbidden: '当前身份无权使用所选服务、环境或操作',
  version_conflict: '需求已更新，请重新读取详情后操作',
  idempotency_conflict: '请求标识已用于不同内容，请重新读取详情',
  snapshot_in_progress: '该需求正在固定快照，请稍后重新读取详情',
  validation_error: '请检查需求名称、服务和分支输入',
  invalid_json: '请求内容无效，请重新填写',
  not_found: '需求不存在或当前身份不可见',
  unauthenticated: '身份已失效，请重新登录',
}
