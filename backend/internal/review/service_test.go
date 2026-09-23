package review

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestServiceReadsDiffFromAReadyWorkspace(t *testing.T) {
	tasks := task.NewStore()
	created, err := tasks.Create(task.CreateInput{
		RequestID:      "req-create-task-1",
		TenantID:       "tenant-a",
		IdempotencyKey: "create-task-1",
		Type:           "PR_REVIEW",
		Goal:           "Review pull request 42",
		Repository: task.RepositoryReference{
			Provider:     "gitlab",
			RepositoryID: "project-7",
			BaseSHA:      "1111111111111111111111111111111111111111",
			HeadSHA:      "2222222222222222222222222222222222222222",
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	queued, err := tasks.Transition(task.TransitionInput{
		TaskID:          created.Task.ID,
		RequestID:       "req-queue-task-1",
		TenantID:        "tenant-a",
		IdempotencyKey:  "queue-task-1",
		ExpectedVersion: created.Task.Version,
		Status:          task.StatusQueued,
	})
	if err != nil {
		t.Fatalf("queue task: %v", err)
	}
	manager, err := workspace.NewManagerWithPreparer(
		tasks,
		referenceVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create Workspace Manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), workspace.RegisterInput{
		TenantID:       queued.TenantID,
		TaskID:         queued.ID,
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register Workspace: %v", err)
	}
	ready, err := manager.Prepare(context.Background(), workspace.PrepareInput{
		TenantID:        queued.TenantID,
		TaskID:          queued.ID,
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	})
	if err != nil {
		t.Fatalf("prepare Workspace: %v", err)
	}

	source := &recordingDiffSource{patch: []byte("diff --git a/README.md b/README.md\n")}
	result, err := NewService(manager, source).Get(context.Background(), GetInput{
		TaskID:   queued.ID,
		TenantID: queued.TenantID,
	})
	if err != nil {
		t.Fatalf("get review diff: %v", err)
	}

	if source.worktreePath != ready.Workspace.Path || source.baseSHA != ready.Workspace.BaseSHA || source.headSHA != ready.Workspace.HeadSHA {
		t.Fatalf("expected the READY Workspace coordinates, got %#v", source)
	}
	expectedSHA := fmt.Sprintf("%x", sha256.Sum256(source.patch))
	if result.TaskID != queued.ID || result.WorkspaceID != ready.Workspace.ID {
		t.Fatalf("unexpected diff ownership: %#v", result)
	}
	if result.BaseSHA != ready.Workspace.BaseSHA || result.HeadSHA != ready.Workspace.HeadSHA {
		t.Fatalf("unexpected immutable refs: %#v", result)
	}
	if result.MediaType != "text/x-diff" || result.SHA256 != expectedSHA || result.SizeBytes != int64(len(source.patch)) {
		t.Fatalf("unexpected diff metadata: %#v", result)
	}
	if result.Patch != string(source.patch) {
		t.Fatalf("unexpected patch: %q", result.Patch)
	}
}

type recordingDiffSource struct {
	worktreePath string
	baseSHA      string
	headSHA      string
	patch        []byte
}

func (s *recordingDiffSource) Read(_ context.Context, input repository.DiffInput) ([]byte, error) {
	s.worktreePath = input.WorktreePath
	s.baseSHA = input.BaseSHA
	s.headSHA = input.HeadSHA
	return s.patch, nil
}

type referenceVerifierFunc func(context.Context, task.RepositoryReference) error

func (verify referenceVerifierFunc) Verify(ctx context.Context, reference task.RepositoryReference) error {
	return verify(ctx, reference)
}

type workspacePreparerFunc func(context.Context, task.RepositoryReference, string) error

func (prepare workspacePreparerFunc) Prepare(ctx context.Context, reference task.RepositoryReference, destination string) error {
	return prepare(ctx, reference, destination)
}

var _ repository.ReferenceVerifier = referenceVerifierFunc(nil)
