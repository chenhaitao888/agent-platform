import { vi } from 'vitest'
import type { Task } from '../task'
import type { Workspace } from '../workspace'

// Test-only Node implementation of the browser Web Crypto API; no dependency
// or Node ambient types are added to the browser project.
const { createHash, webcrypto } = await vi.importActual<{
  webcrypto: Crypto
  createHash: (algorithm: string) => { update: (text: string) => { digest: (encoding: string) => string } }
}>('node:crypto')

export const reviewTask: Task = {
  id: 'task-1', tenantId: 'tenant-a', idempotencyKey: 'create-task-1',
  type: 'PR_REVIEW', goal: 'Review controlled change', status: 'QUEUED', version: 2,
  repository: { provider: 'gitlab', repositoryId: 'platform/project-7', targetBranch: 'master',
    targetSha: '3'.repeat(40), baseSha: '1'.repeat(40), headSha: '2'.repeat(40) },
  createdAt: '2026-10-22T00:00:00Z',
}

export const reviewWorkspace: Workspace = {
  id: 'workspace-1', tenantId: reviewTask.tenantId, taskId: reviewTask.id,
  repository: reviewTask.repository, targetBranch: 'master', targetSha: reviewTask.repository.targetSha,
  baseSha: reviewTask.repository.baseSha, headSha: reviewTask.repository.headSha,
  state: 'READY', version: 3, path: '/tmp/controlled-worktree', createdAt: reviewTask.createdAt,
}

export const findingsReport = {
  schemaVersion: '1.0', baseSha: reviewWorkspace.baseSha, headSha: reviewWorkspace.headSha,
  findings: [{ title: 'Restore the zero guard', description: 'A zero count now throws.',
    severity: 'HIGH', confidence: 0.95, path: 'divider.py', startLine: 2, endLine: 2 }],
}
export const findingsText = JSON.stringify(findingsReport)
export function findingsArtifact(text = findingsText) {
  return { id: 'artifact-1', tenantId: reviewTask.tenantId, taskId: reviewTask.id,
    workspaceId: reviewWorkspace.id, type: 'PR_REVIEW_FINDINGS', mediaType: 'application/json',
    sizeBytes: new TextEncoder().encode(text).length, sha256: createHash('sha256').update(text).digest('hex'), createdAt: reviewTask.createdAt }
}

export function reviewExecution(state = 'SUCCEEDED', id = 'review-1') {
  return { id, tenantId: reviewTask.tenantId, taskId: reviewTask.id, workspaceId: reviewWorkspace.id,
    workspaceVersion: reviewWorkspace.version, baseSha: reviewWorkspace.baseSha, headSha: reviewWorkspace.headSha,
    state, startedAt: reviewTask.createdAt, ...(state === 'RUNNING' ? {} : { completedAt: reviewTask.createdAt }),
    ...(state === 'SUCCEEDED' ? { artifact: findingsArtifact() } : {}) }
}

export function deferredResponse() {
  let resolve!: (response: Response) => void
  let reject!: (error: Error) => void
  const promise = new Promise<Response>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

export function installReviewCrypto() { vi.stubGlobal('crypto', webcrypto) }
