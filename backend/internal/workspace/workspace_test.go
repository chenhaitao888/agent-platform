package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/backend/internal/task"
)

func TestPrepareMakesTheWorkspaceReadyAtAnOwnedPath(t *testing.T) {
	tasks := queuedTaskStore(t)
	preparer := &blockingPreparer{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	workspaceRoot := t.TempDir()
	manager, err := NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		preparer,
		workspaceRoot,
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}

	type prepareOutcome struct {
		result PrepareResult
		err    error
	}
	outcome := make(chan prepareOutcome, 1)
	go func() {
		result, prepareErr := manager.Prepare(context.Background(), PrepareInput{
			TenantID:        "tenant-a",
			TaskID:          "task-1",
			IdempotencyKey:  "prepare-workspace-1",
			ExpectedVersion: registered.Workspace.Version,
		})
		outcome <- prepareOutcome{result: result, err: prepareErr}
	}()

	destination := <-preparer.started
	preparing, ok := manager.Get("task-1", "tenant-a")
	if !ok {
		t.Fatal("expected workspace while preparation is running")
	}
	if preparing.State != StatePreparing || preparing.Version != 2 {
		t.Fatalf("expected PREPARING version 2, got %#v", preparing)
	}
	if preparing.Path != "" {
		t.Fatalf("expected no usable path before preparation finishes, got %q", preparing.Path)
	}
	// 类比 Java 中异步任务的状态通知：外部操作尚未完成时，开始事件就应可见。
	events, ok := tasks.ListEvents("task-1", "tenant-a")
	if !ok || len(events) != 4 {
		t.Fatalf("expected four events while preparation is running, got %#v", events)
	}
	if events[3].EventType != task.EventTypeWorkspacePreparing || events[3].Payload.Workspace == nil || events[3].Payload.Workspace.State != string(StatePreparing) {
		t.Fatalf("expected a visible PREPARING event before completion, got %#v", events[3])
	}

	close(preparer.release)
	prepared := <-outcome
	if prepared.err != nil {
		t.Fatalf("prepare workspace: %v", prepared.err)
	}
	ready := prepared.result.Workspace
	if ready.State != StateReady || ready.Version != 3 {
		t.Fatalf("expected READY version 3, got %#v", ready)
	}
	if ready.Path != destination {
		t.Fatalf("expected path %q, got %q", destination, ready.Path)
	}
	resolvedRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		t.Fatalf("resolve test workspace root: %v", err)
	}
	if ready.Path != filepath.Join(resolvedRoot, "workspace-1", "worktree") {
		t.Fatalf("expected manager-owned workspace path, got %q", ready.Path)
	}
	events, ok = tasks.ListEvents("task-1", "tenant-a")
	if !ok || len(events) != 5 || events[4].EventType != task.EventTypeWorkspaceReady {
		t.Fatalf("expected READY to be the next event, got %#v", events)
	}
}

func TestPrepareReplaysASuccessfulIdempotentRequestWithoutPreparingAgain(t *testing.T) {
	tasks := queuedTaskStore(t)
	preparer := &countingPreparer{}
	manager, err := NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		preparer,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	input := PrepareInput{
		TenantID:        "tenant-a",
		TaskID:          "task-1",
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	}

	first, err := manager.Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	replay, err := manager.Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("replay preparation: %v", err)
	}
	if !first.Prepared || replay.Prepared {
		t.Fatalf("expected first call to prepare and replay to reuse it: first=%#v replay=%#v", first, replay)
	}
	if replay.Workspace != first.Workspace {
		t.Fatalf("expected replayed result %#v, got %#v", first.Workspace, replay.Workspace)
	}
	if preparer.calls != 1 {
		t.Fatalf("expected one preparation, got %d", preparer.calls)
	}
}

func TestRegisterReplayReturnsTheCurrentWorkspaceAfterPreparation(t *testing.T) {
	tasks := queuedTaskStore(t)
	manager, err := NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		&countingPreparer{},
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registerInput := RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	}
	registered, err := manager.Register(context.Background(), registerInput)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	ready, err := manager.Prepare(context.Background(), PrepareInput{
		TenantID:        "tenant-a",
		TaskID:          "task-1",
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	})
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}

	replayed, err := manager.Register(context.Background(), registerInput)
	if err != nil {
		t.Fatalf("replay registration: %v", err)
	}
	if replayed.Created {
		t.Fatal("expected replay not to create another Workspace")
	}
	if replayed.Workspace != ready.Workspace {
		t.Fatalf("expected current Workspace %#v, got %#v", ready.Workspace, replayed.Workspace)
	}
}

func TestPrepareFailureRestoresARegisteredWorkspaceForRetry(t *testing.T) {
	tasks := queuedTaskStore(t)
	manager, err := NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return errors.New("clone failed")
		}),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}

	_, err = manager.Prepare(context.Background(), PrepareInput{
		TenantID:        "tenant-a",
		TaskID:          "task-1",
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	})
	if !errors.Is(err, ErrPreparationFailed) {
		t.Fatalf("expected preparation failure, got %v", err)
	}
	restored, ok := manager.Get("task-1", "tenant-a")
	if !ok {
		t.Fatal("expected Workspace to remain available after failure")
	}
	if restored.State != StateRegistered || restored.Version != 3 || restored.Path != "" {
		t.Fatalf("expected retryable REGISTERED version 3 without a path, got %#v", restored)
	}
}

func TestPrepareFailureDoesNotRecreateARemovedWorkspace(t *testing.T) {
	tasks := queuedTaskStore(t)
	var manager *Manager
	// 目前没有删除 API；用准备器回调模拟将来删除发生在外部操作期间。
	preparer := workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
		manager.mu.Lock()
		delete(manager.byTask, "task-1")
		manager.mu.Unlock()
		return errors.New("clone failed")
	})
	var err error
	manager, err = NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		preparer,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}

	_, err = manager.Prepare(context.Background(), PrepareInput{
		TenantID:        "tenant-a",
		TaskID:          "task-1",
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	})
	if !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("expected missing Workspace error, got %v", err)
	}
	if _, exists := manager.Get("task-1", "tenant-a"); exists {
		t.Fatal("preparation failure must not recreate a removed Workspace")
	}
}

func TestPrepareSuccessDoesNotRecreateARemovedWorkspace(t *testing.T) {
	tasks := queuedTaskStore(t)
	var manager *Manager
	preparer := workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
		manager.mu.Lock()
		delete(manager.byTask, "task-1")
		manager.mu.Unlock()
		return nil
	})
	var err error
	manager, err = NewManagerWithPreparer(
		tasks,
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		preparer,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create workspace manager: %v", err)
	}
	registered, err := manager.Register(context.Background(), RegisterInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		IdempotencyKey: "register-workspace-1",
	})
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}

	_, err = manager.Prepare(context.Background(), PrepareInput{
		TenantID:        "tenant-a",
		TaskID:          "task-1",
		IdempotencyKey:  "prepare-workspace-1",
		ExpectedVersion: registered.Workspace.Version,
	})
	if !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("expected missing Workspace error, got %v", err)
	}
	if _, exists := manager.Get("task-1", "tenant-a"); exists {
		t.Fatal("preparation success must not recreate a removed Workspace")
	}
}

func TestNewManagerWithPreparerRejectsARootSymlinkToTheFilesystemRoot(t *testing.T) {
	rootLink := filepath.Join(t.TempDir(), "workspace-root")
	if err := os.Symlink(string(filepath.Separator), rootLink); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	_, err := NewManagerWithPreparer(
		task.NewStore(),
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		&countingPreparer{},
		rootLink,
	)
	if err == nil {
		t.Fatal("expected a Workspace root resolving to the filesystem root to be rejected")
	}
}

type blockingPreparer struct {
	started chan string
	release chan struct{}
}

type countingPreparer struct {
	calls int
}

type workspacePreparerFunc func(context.Context, task.RepositoryReference, string) error

func (prepare workspacePreparerFunc) Prepare(ctx context.Context, reference task.RepositoryReference, destination string) error {
	return prepare(ctx, reference, destination)
}

func (p *countingPreparer) Prepare(context.Context, task.RepositoryReference, string) error {
	p.calls++
	return nil
}

func (p *blockingPreparer) Prepare(_ context.Context, _ task.RepositoryReference, destination string) error {
	p.started <- destination
	<-p.release
	return nil
}

type repositoryVerifierFunc func(context.Context, task.RepositoryReference) error

func (verify repositoryVerifierFunc) Verify(ctx context.Context, reference task.RepositoryReference) error {
	return verify(ctx, reference)
}

func queuedTaskStore(t *testing.T) *task.Store {
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
	_, err = tasks.Transition(task.TransitionInput{
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
	return tasks
}
