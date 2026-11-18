export type CompanyOperation = {
  state: string; errorCode?: string
  ticket?: { id: string; url?: string; changeId: string; changeVersion: string }
  observation?: { state: string; observedAt: string; effectiveVersion?: string; publication?: { id: string; state: string } }
}
const states: Record<string, string> = {
  NOT_SUBMITTED: '等待保存并提单', SUBMITTING: '正在提交公司工单', SUBMISSION_UNKNOWN: '提单结果待核对',
  WAITING_APPROVAL: '等待公司审批', WAITING_PUBLICATION: '公司审批通过，等待自动发布', PUBLISHING: '公司平台正在发布',
  PUBLISHED: '已发布，测试环境版本与配置值核验通过', APPROVAL_REJECTED: '公司审批已拒绝',
  APPROVAL_WITHDRAWN: '公司工单已撤回', APPROVAL_EXPIRED: '公司工单已失效', PUBLICATION_FAILED: '公司自动发布失败',
  PUBLICATION_DRIFTED: '发布记录与测试环境配置不一致', BLOCKED: '已停止提单',
}
const errors: Record<string, string> = {
  ticket_outcome_unknown: '正在按原请求查询工单，不会重复提单。',
  tracking_permission_or_binding_changed: '读取权限或连接绑定发生变化，追踪已暂停；恢复后继续查询。',
  tracking_unavailable: '暂时无法查询公司平台，稍后自动重试。',
  tracking_evidence_invalid: '工单、变更版本或发布证据不匹配，需要检查绑定和公司记录。',
  approval_inactive: '本地保存/提单许可已失效，尚未发送的提单已停止。',
  authorization_or_input_changed: '权限或输入已变化，尚未发送的提单已停止。',
  save_incomplete: '保存尚未全部完成，未发起新的工单。', submission_not_started: '未发起工单，请检查保存结果后重新准备计划。',
}
function safeURL(value?: string) {
  if (!value) return undefined
  try { const u = new URL(value); return u.protocol === 'https:' && !u.username && !u.password ? u.href : undefined } catch { return undefined }
}
export default function CompanyStatus({ operation }: { operation: CompanyOperation }) {
  const url = safeURL(operation.ticket?.url)
  return <div className="company-status" aria-label="公司审批与发布">
    <p><strong>{states[operation.state] ?? '公司状态待核对'}</strong></p>
    {operation.errorCode && <p role="status">{errors[operation.errorCode] ?? '公司流程需要核对。'}</p>}
    {operation.ticket && <><p>工单：{url ? <a href={url} target="_blank" rel="noreferrer">{operation.ticket.id}</a> : operation.ticket.id}</p><p>变更 {operation.ticket.changeId} · 版本 {operation.ticket.changeVersion}</p></>}
    {operation.observation?.publication && <p>发布记录 {operation.observation.publication.id}</p>}
    {operation.observation?.effectiveVersion && <p>测试环境生效版本：{operation.observation.effectiveVersion}</p>}
    {operation.observation && <p>最近核验：{operation.observation.observedAt}</p>}
  </div>
}
