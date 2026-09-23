package review

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestServiceReadsDiffFromAReadyWorkspace(t *testing.T) {
	manager, ready, tasks := readyWorkspaceFixture(t)
	source := &recordingDiffSource{patch: []byte("diff --git a/README.md b/README.md\n")}
	result, err := NewService(manager, source, tasks).Get(context.Background(), GetInput{
		TaskID:   ready.TaskID,
		TenantID: ready.TenantID,
	})
	if err != nil {
		t.Fatalf("get review diff: %v", err)
	}

	if source.worktreePath != ready.Path || source.baseSHA != ready.BaseSHA || source.headSHA != ready.HeadSHA {
		t.Fatalf("expected the READY Workspace coordinates, got %#v", source)
	}
	expectedSHA := fmt.Sprintf("%x", sha256.Sum256(source.patch))
	if result.TaskID != ready.TaskID || result.WorkspaceID != ready.ID {
		t.Fatalf("unexpected diff ownership: %#v", result)
	}
	if result.BaseSHA != ready.BaseSHA || result.HeadSHA != ready.HeadSHA {
		t.Fatalf("unexpected immutable refs: %#v", result)
	}
	if result.MediaType != "text/x-diff" || result.SHA256 != expectedSHA || result.SizeBytes != int64(len(source.patch)) {
		t.Fatalf("unexpected diff metadata: %#v", result)
	}
	if result.Patch != string(source.patch) {
		t.Fatalf("unexpected patch: %q", result.Patch)
	}
}

func TestServiceArchivesTheDiffFromAReadyWorkspace(t *testing.T) {
	manager, ready, tasks := readyWorkspaceFixture(t)
	patch := []byte("diff --git a/README.md b/README.md\n-base\n+head\n")
	source := &recordingDiffSource{patch: patch}
	artifacts := artifact.NewStore()
	service := NewServiceWithArtifactStore(manager, source, artifacts, tasks)

	result, err := service.Archive(context.Background(), ArchiveInput{
		TaskID:                   ready.TaskID,
		TenantID:                 ready.TenantID,
		IdempotencyKey:           "archive-diff-1",
		ExpectedWorkspaceVersion: ready.Version,
	})
	if err != nil {
		t.Fatalf("archive review diff: %v", err)
	}

	if !result.Created {
		t.Fatal("expected the first archive request to create an Artifact")
	}
	if result.Artifact.TaskID != ready.TaskID || result.Artifact.WorkspaceID != ready.ID {
		t.Fatalf("unexpected Artifact ownership: %#v", result.Artifact)
	}
	if result.Artifact.Type != artifact.TypeRepositoryDiff || result.Artifact.MediaType != "text/x-diff" {
		t.Fatalf("unexpected Artifact type: %#v", result.Artifact)
	}
	_, storedContent, ok := artifacts.GetContent(result.Artifact.ID, ready.TenantID)
	if !ok || string(storedContent) != string(patch) {
		t.Fatalf("expected archived diff content, got %q", storedContent)
	}
	if source.worktreePath != ready.Path || source.baseSHA != ready.BaseSHA || source.headSHA != ready.HeadSHA {
		t.Fatalf("expected the READY Workspace coordinates, got %#v", source)
	}
}

func TestServiceRejectsAStaleWorkspaceVersionBeforeReadingDiff(t *testing.T) {
	manager, ready, tasks := readyWorkspaceFixture(t)
	source := &recordingDiffSource{patch: []byte("must not be read")}
	service := NewServiceWithArtifactStore(manager, source, artifact.NewStore(), tasks)

	_, err := service.Archive(context.Background(), ArchiveInput{
		TaskID:                   ready.TaskID,
		TenantID:                 ready.TenantID,
		IdempotencyKey:           "archive-diff-stale",
		ExpectedWorkspaceVersion: ready.Version - 1,
	})
	if !errors.Is(err, workspace.ErrVersionConflict) {
		t.Fatalf("expected Workspace version conflict, got %v", err)
	}
	if source.worktreePath != "" {
		t.Fatalf("expected Git not to run for a stale version, got path %q", source.worktreePath)
	}
}

func readyWorkspaceFixture(t *testing.T) (*workspace.Manager, workspace.Workspace, *task.Store) {
	t.Helper()
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
	return manager, ready.Workspace, tasks
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
