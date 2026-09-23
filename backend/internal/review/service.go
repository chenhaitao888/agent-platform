package review

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"agent-platform/backend/internal/repository"
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
}

func NewService(workspaces *workspace.Manager, source repository.DiffReader) *Service {
	return &Service{workspaces: workspaces, source: source}
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
