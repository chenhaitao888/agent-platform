import type { RepositoryReference } from './task'

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
  repository?: RepositoryReference
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
  snapshot_in_progress: '该需求正在固定快照、审阅或生成计划，请稍后刷新状态',
  snapshot_not_ready: '请选择该需求下完整可用的代码快照',
  review_complete: '该快照的所有服务已完成审阅；代码变化后请创建新快照',
  review_unavailable: '当前部署尚未启用代码审阅',
  request_canceled: '本次请求已中止，请读取状态后重试未完成服务',
  validation_error: '请检查需求名称、服务和分支输入',
  invalid_json: '请求内容无效，请重新填写',
  not_found: '需求不存在或当前身份不可见',
  unauthenticated: '身份已失效，请重新登录',
}

export type ServiceReview = {
  serviceId: string
  state: 'NOT_STARTED' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'CANCELED'
  stage: 'admission' | 'preparation' | 'review' | 'complete'
  taskId?: string
  workspaceId?: string
  workspaceVersion?: number
  executionId?: string
  artifactId?: string
  errorCode?: string
  reused?: boolean
}
export type ChangeReviewRun = {
  id: string
  snapshotId: string
  snapshotHash: string
  state: 'RUNNING' | 'SUCCEEDED' | 'PARTIAL' | 'FAILED' | 'CANCELED'
  services: ServiceReview[]
  startedAt: string
  completedAt?: string
}
export type ChangeReviewResult = { changeId: string; changeVersion: number; review: ChangeReviewRun }
