export type PlannedValue = { type: 'string' | 'boolean' | 'integer' | 'number' | 'secretRef'; text?: string; secretRef?: string; secretVersion?: string }
export type ExperimentSpec = { variants: { name: string; weight: number }[]; audience: string; metrics: string[]; allocation: string; exclusionGroup?: string }
export type PlanRequirement = {
  serviceId: string
  config?: { key: string; desired?: PlannedValue; reason: string }[]
  experiments?: { key: string; desired?: ExperimentSpec; reason: string }[]
  configNotInvolved?: string
  experimentNotInvolved?: string
}
export type Evidence = { source: string; serviceId: string; headSha: string; path?: string; line?: number; reason?: string }
export type PlanSource = {
  serviceId: string; headSha: string; baseSha: string
  configCoverage: string; experimentCoverage: string; configReason?: string; experimentReason?: string
  review: { taskId?: string; executionId?: string; artifactId?: string; state: string }
  candidates: { kind: string; key: string; evidence: Evidence[] }[]
}
export type PlanAction = {
  id: string; hash: string; kind: string; key: string; operation: string; effect: string
  target: { tenantId: string; environment: string; connectorRef: string; resourceRef: string }
  resourceVersion: string; expectedVersion?: string; mustNotExist: boolean
  currentValue?: PlannedValue; currentExperiment?: ExperimentSpec
  proposals: { serviceId: string; value?: PlannedValue; experiment?: ExperimentSpec; evidence: Evidence[] }[]
}
export type ChangePlan = {
  id: string; revision: number; tenantId: string; environment: string; snapshotId: string; snapshotHash: string
  schemaVersion: string; state: 'RUNNING' | 'READY_FOR_REVIEW' | 'BLOCKED'; hash: string; createdAt: string
  services: PlanSource[]; actions: PlanAction[]; issues: { code: string; serviceId?: string; kind?: string; key?: string }[]
}
export const coverageLabels: Record<string, string> = { NOT_ANALYZED: '尚未分析', ANALYSIS_FAILED: '分析失败', NO_CANDIDATES: '未发现候选，待确认覆盖', NOT_INVOLVED: '已确认不涉及', CANDIDATES_FOUND: '发现变更候选' }
export const operationLabels: Record<string, string> = { 'config.create': '新增配置', 'config.update': '更新配置', 'config.no_change': '配置已符合要求', 'experiment.create_draft': '新增未激活实验草稿', 'experiment.update_draft': '更新未激活实验草稿' }
export const planIssues: Record<string, string> = {
  coverage_confirmation_required: '请核对需求与代码，补充变更要求或说明不涉及的依据',
  coverage_conflict: '已发现候选，与“不涉及”的声明冲突',
  desired_value_required: '配置期望值尚未确定，请补齐业务值与理由',
  experiment_fields_required: '请补齐实验分组、受众、指标和分配规则',
  review_incomplete: '该快照的服务审阅尚未完成',
  workspace_required: '请先发起该快照的审阅，准备固定代码工作区',
  diff_unavailable: '固定版本差异读取失败，请检查工作区后生成新计划',
  analysis_failed: '候选分析失败，不能据此确认没有变更',
  analysis_canceled: '分析已中止，请生成新计划重试',
  binding_unavailable: '请由管理员登记该服务和环境的目标资源',
  platform_read_failed: '平台现状读取失败或返回内容无法安全使用',
  config_type_mismatch: '期望类型与平台现有配置类型不一致',
  config_value_out_of_range: '期望值超出平台允许范围',
  config_effect_unknown: '平台保存与生效语义尚未明确',
  conditional_update_unsupported: '平台不支持所需的条件更新，无法形成安全更新计划',
  secret_reference_required: '敏感配置只能使用密钥引用和版本',
  secret_reference_unregistered: '密钥引用或版本不在该资源登记的允许范围内',
  shared_resource_conflict: '多个服务对同一资源提出了不同要求，请统一后生成新计划',
  inactive_draft_unsupported: '平台不能保证草稿不会分流，已阻断实验计划',
  experiment_not_draft: '已有实验不是草稿，不能作为草稿更新',
  allocation_unsupported: '平台不支持此分配规则',
  exclusion_unsupported: '平台不支持要求的互斥规则',
}
export const valueText = (value?: PlannedValue) => !value ? '待补齐' : value.type === 'secretRef' ? `密钥引用 ${value.secretRef} · 版本 ${value.secretVersion}` : value.text === '' ? '空字符串' : value.text ?? ''
