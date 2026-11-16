import type { ChangeSnapshot } from './change'
import type { ChangePlan } from './planning'
import type { ReviewExecution } from './review'

export type DeliveryBundle = {
  schemaVersion: 'delivery-archive.v1'; id: string; tenantId: string; changeId: string
  title: string; description: string; environment: string; createdBy: string; createdAt: string; digest: string
  snapshot: ChangeSnapshot; plan: ChangePlan
  evidence: { serviceId: string; execution: ReviewExecution }[]
}
export const archiveErrors: Record<string, string> = {
  archive_unavailable: '交付包存储尚未启用或暂时不可用，请稍后重试。',
  archive_conflict: '计划摘要或请求标识不一致，请重新读取计划。',
  archive_source_invalid: '计划与审阅证据不完整或不一致，暂时无法保存。',
  archive_integrity_failed: '交付包完整性校验失败，请联系管理员检查存储。',
  service_forbidden: '当前身份无权保存或读取此交付包。',
  not_found: '记录不存在；如果服务已重启，请从交付包列表查找已保存的记录。',
}
