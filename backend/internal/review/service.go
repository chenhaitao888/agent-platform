package review

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

var (
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrWorkspaceNotReady = errors.New("workspace is not ready")
)

type GetInput struct {
	TaskID   string
	TenantID string
}

type ArchiveInput struct {
	RequestID                string
	TaskID                   string
	TenantID                 string
	IdempotencyKey           string
	ExpectedWorkspaceVersion uint64
}

type Diff struct {
	TaskID      string `json:"taskId"`
	WorkspaceID string `json:"workspaceId"`
	BaseSHA     string `json:"baseSha"`
	HeadSHA     string `json:"headSha"`
	MediaType   string `json:"mediaType"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
	Patch       string `json:"patch"`
}

type Service struct {
	workspaces *workspace.Manager
	source     repository.DiffReader
	artifacts  *artifact.Store
	tasks      *task.Store
}

func NewService(workspaces *workspace.Manager, source repository.DiffReader, tasks *task.Store) *Service {
	return NewServiceWithArtifactStore(workspaces, source, artifact.NewStore(), tasks)
}

func NewServiceWithArtifactStore(workspaces *workspace.Manager, source repository.DiffReader, artifacts *artifact.Store, tasks *task.Store) *Service {
	return &Service{workspaces: workspaces, source: source, artifacts: artifacts, tasks: tasks}
}

func (s *Service) Get(ctx context.Context, input GetInput) (Diff, error) {
	current, ok := s.workspaces.Get(input.TaskID, input.TenantID)
	if !ok {
		return Diff{}, ErrWorkspaceNotFound
	}
	if current.State != workspace.StateReady || current.Path == "" {
		return Diff{}, ErrWorkspaceNotReady
	}

	// 路径和 SHA 全部来自 READY Workspace。HTTP 请求只提供 task/tenant，
	// 因而不能偷偷把 diff 改成另一组 revision 或另一个本地目录。
	patch, err := s.source.Read(ctx, repository.DiffInput{
		WorktreePath: current.Path,
		BaseSHA:      current.BaseSHA,
		HeadSHA:      current.HeadSHA,
	})
	if err != nil {
		return Diff{}, fmt.Errorf("read immutable repository diff: %w", err)
	}
	digest := sha256.Sum256(patch)
	return Diff{
		TaskID:      current.TaskID,
		WorkspaceID: current.ID,
		BaseSHA:     current.BaseSHA,
		HeadSHA:     current.HeadSHA,
		MediaType:   "text/x-diff",
		SHA256:      fmt.Sprintf("%x", digest),
		SizeBytes:   int64(len(patch)),
		Patch:       string(patch),
	}, nil
}

func (s *Service) Archive(ctx context.Context, input ArchiveInput) (artifact.CreateResult, error) {
	current, ok := s.workspaces.Get(input.TaskID, input.TenantID)
	if !ok {
		return artifact.CreateResult{}, ErrWorkspaceNotFound
	}
	if current.State != workspace.StateReady || current.Path == "" {
		return artifact.CreateResult{}, ErrWorkspaceNotReady
	}
	if current.Version != input.ExpectedWorkspaceVersion {
		return artifact.CreateResult{}, workspace.ErrVersionConflict
	}

	// 和预览一样，归档时重新从 READY Workspace 读取可信坐标；浏览器提交的只有
	// task、tenant、幂等键和乐观锁版本，不能把任意内容伪装成平台 Artifact。
	patch, err := s.source.Read(ctx, repository.DiffInput{
		WorktreePath: current.Path,
		BaseSHA:      current.BaseSHA,
		HeadSHA:      current.HeadSHA,
	})
	if err != nil {
		return artifact.CreateResult{}, fmt.Errorf("read immutable repository diff for Artifact: %w", err)
	}
	result, err := s.artifacts.Create(artifact.CreateInput{
		TenantID:       current.TenantID,
		TaskID:         current.TaskID,
		WorkspaceID:    current.ID,
		IdempotencyKey: input.IdempotencyKey,
		Type:           artifact.TypeRepositoryDiff,
		MediaType:      "text/x-diff",
		Content:        patch,
	})
	if err != nil {
		return artifact.CreateResult{}, fmt.Errorf("create diff Artifact: %w", err)
	}
	if result.Created {
		// 幂等重放拿到同一个 Artifact，但“创建”这一事实只能追加一次。
		// 只记录元数据，不把最多 1 MiB 的 patch 或内部 Git 输出写入事件。
		s.tasks.AppendEvent(task.AppendEventInput{
			TenantID:    result.Artifact.TenantID,
			TaskID:      result.Artifact.TaskID,
			EventType:   task.EventTypeArtifactCreated,
			CausationID: input.RequestID,
			OccurredAt:  result.Artifact.CreatedAt,
			Payload: task.EventPayload{Artifact: &task.ArtifactEventPayload{
				ArtifactID:  result.Artifact.ID,
				WorkspaceID: result.Artifact.WorkspaceID,
				Type:        string(result.Artifact.Type),
				MediaType:   result.Artifact.MediaType,
				SHA256:      result.Artifact.SHA256,
				SizeBytes:   result.Artifact.SizeBytes,
			}},
		})
	}
	return result, nil
}
