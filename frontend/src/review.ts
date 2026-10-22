import type { Artifact } from './artifact'
import type { Workspace } from './workspace'

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

export type FindingsArtifact = Extract<Artifact, { type: 'PR_REVIEW_FINDINGS' }>
export type ReviewExecution = {
  id: string
  tenantId: string
  taskId: string
  workspaceId: string
  workspaceVersion: number
  baseSha: string
  headSha: string
  state: 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'CANCELED'
  startedAt: string
  completedAt?: string
  artifact?: FindingsArtifact
  errorCode?: string
}
export type Finding = {
  title: string; description: string
  severity: 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL'
  confidence: number; path: string; startLine: number; endLine: number
}
export type FindingsReport = {
  schemaVersion: '1.0'; baseSha: string; headSha: string; findings: Finding[]
}

export class ReviewResponseError extends Error {}
const maxFindingsBytes = 256 * 1024
const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)
const nonempty = (value: unknown): value is string => typeof value === 'string' && value.trim().length > 0
const integer = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value > 0

export function parseReviewExecution(value: unknown, workspace: Workspace): ReviewExecution {
  const invalid = () => new ReviewResponseError('审阅记录与当前 Workspace 不一致或格式无效，请重新加载。')
  if (!object(value) || !nonempty(value.id) || value.tenantId !== workspace.tenantId ||
    value.taskId !== workspace.taskId || value.workspaceId !== workspace.id || value.workspaceVersion !== workspace.version ||
    value.baseSha !== workspace.baseSha || value.headSha !== workspace.headSha ||
    typeof value.state !== 'string' || !['RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELED'].includes(value.state) || !nonempty(value.startedAt)) throw invalid()
  if (value.state !== 'RUNNING' && !nonempty(value.completedAt)) throw invalid()
  if (value.state === 'SUCCEEDED') {
    const artifact = value.artifact
    if (!object(artifact) || !nonempty(artifact.id) || artifact.tenantId !== workspace.tenantId ||
      artifact.taskId !== workspace.taskId || artifact.workspaceId !== workspace.id ||
      artifact.type !== 'PR_REVIEW_FINDINGS' || artifact.mediaType !== 'application/json' ||
      typeof artifact.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(artifact.sha256) ||
      !integer(artifact.sizeBytes) || artifact.sizeBytes > maxFindingsBytes || !nonempty(artifact.createdAt)) throw invalid()
  } else if (value.artifact !== undefined) throw invalid()
  if (value.errorCode !== undefined && typeof value.errorCode !== 'string') throw invalid()
  return value as ReviewExecution
}

export async function parseFindingsContent(response: Response, execution: ReviewExecution): Promise<FindingsReport> {
  const invalid = () => new ReviewResponseError('审阅结果内容校验失败，请刷新审阅状态后重试。')
  const bytes = new Uint8Array(await response.arrayBuffer())
  if (!execution.artifact || bytes.length > maxFindingsBytes || bytes.length !== execution.artifact.sizeBytes) throw invalid()
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
  if (Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('') !== execution.artifact.sha256) throw invalid()
  let value: unknown
  try { value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) } catch { throw invalid() }
  if (!object(value) || value.schemaVersion !== '1.0' || value.baseSha !== execution.baseSha || value.headSha !== execution.headSha ||
    !Array.isArray(value.findings) || value.findings.length > 50) throw invalid()
  for (const f of value.findings) {
    if (!object(f) || !nonempty(f.title) || [...f.title].length > 256 || !nonempty(f.description) || [...f.description].length > 4096 ||
      typeof f.severity !== 'string' || !['LOW', 'MEDIUM', 'HIGH', 'CRITICAL'].includes(f.severity) || typeof f.confidence !== 'number' ||
      !Number.isFinite(f.confidence) || f.confidence < 0 || f.confidence > 1 || !nonempty(f.path) || [...f.path].length > 1024 ||
      /[\\:]/.test(f.path) || [...f.path].some((char) => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127) ||
      f.path.split('/').some((part) => part === '' || part === '.' || part === '..') ||
      !integer(f.startLine) || !integer(f.endLine) || f.endLine < f.startLine || f.endLine > 2147483647) throw invalid()
  }
  return value as FindingsReport
}

const errorMessages: Record<string, string> = {
  review_unavailable: '当前服务未启用只读审阅，请联系部署管理员。',
  review_in_progress: '已有审阅正在执行，请刷新审阅状态。',
  version_conflict: 'Workspace 版本已变更，请重新查看 Workspace。',
  workspace_not_ready: 'Workspace 尚未就绪，请重新查看 Workspace。',
  task_not_queued: 'Task 尚未加入队列。',
  invalid_task_type: '当前 Task 不支持只读审阅。',
  idempotency_conflict: '本次请求与已登记的审阅不一致，请重新查看 Workspace。',
  review_timeout: '审阅超时，请刷新状态后决定是否发起新一轮。',
  review_canceled: '审阅已取消。',
  review_findings_invalid: '模型输出未通过校验，本轮没有可用结果。',
  review_output_unavailable: '本轮未取得可用的模型输出。',
  review_output_too_large: '模型输出超过限制，本轮没有可用结果。',
  diff_unavailable: '暂时无法读取仓库差异。',
  diff_too_large: '仓库差异超过当前审阅限制。',
}
function knownErrorMessage(code: string) { return Object.hasOwn(errorMessages, code) ? errorMessages[code] : undefined }
export function reviewFailureMessage(code?: string) { return knownErrorMessage(code ?? '') ?? '审阅执行失败，请刷新状态后重试。' }

export async function reviewHTTPError(response: Response) {
  let code = ''
  try {
    const value: unknown = await response.json()
    if (object(value) && typeof value.error === 'string') code = value.error
  } catch { /* Never display upstream error bodies or credential echoes. */ }
  const blocked = response.status === 401 || response.status === 403 || ['version_conflict', 'workspace_not_ready', 'task_not_queued', 'invalid_task_type', 'idempotency_conflict'].includes(code)
  const message = response.status === 401 ? '身份已失效，请退出后重新验证身份。'
    : response.status === 403 ? '当前身份无权执行此操作。'
    : response.status === 404 ? '未找到可访问的审阅资源，请重新加载。'
    : knownErrorMessage(code) ?? '请求未完成，请刷新审阅状态或重试本次请求。'
  return { message, blocked }
}
