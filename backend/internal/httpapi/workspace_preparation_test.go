package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestPrepareRegisteredWorkspace(t *testing.T) {
	workspaceRoot := t.TempDir()
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		workspaceRoot,
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace/prepare",
		strings.NewReader(`{
			"requestId":"req-prepare-workspace-1",
			"idempotencyKey":"prepare-workspace-1",
			"tenantId":"tenant-a",
			"expectedVersion":1
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID != "req-prepare-workspace-1" {
		t.Fatalf("expected request ID req-prepare-workspace-1, got %q", requestID)
	}
	var prepared struct {
		State   string `json:"state"`
		Version uint64 `json:"version"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&prepared); err != nil {
		t.Fatalf("decode prepared Workspace: %v", err)
	}
	if prepared.State != "READY" || prepared.Version != 3 {
		t.Fatalf("expected READY version 3, got %#v", prepared)
	}
	if !strings.HasSuffix(prepared.Path, "/workspace-1/worktree") {
		t.Fatalf("expected generated worktree path, got %q", prepared.Path)
	}
}

func TestPrepareWorkspaceFailureReturnsAStableErrorAndRestoresRegistration(t *testing.T) {
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return errors.New("Git output that must stay internal")
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace/prepare",
		strings.NewReader(`{
			"requestId":"req-prepare-workspace-failure",
			"idempotencyKey":"prepare-workspace-failure",
			"tenantId":"tenant-a",
			"expectedVersion":1
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode preparation error: %v", err)
	}
	if body.Error != "workspace_preparation_failed" || body.Message != "workspace preparation failed" {
		t.Fatalf("unexpected preparation error: %#v", body)
	}
	getRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace?tenantId=tenant-a",
		nil,
	)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	var restored struct {
		State   string `json:"state"`
		Version uint64 `json:"version"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restored Workspace: %v", err)
	}
	if restored.State != "REGISTERED" || restored.Version != 3 || restored.Path != "" {
		t.Fatalf("expected retryable REGISTERED version 3, got %#v", restored)
	}
}

func TestPrepareWorkspaceFailureKeepsTheKeyBoundAndAllowsANewAttempt(t *testing.T) {
	var calls atomic.Uint64
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			if calls.Add(1) == 1 {
				return errors.New("private Git diagnostic")
			}
			return nil
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	current := registerPreparationWorkspaceForTest(t, handler, "tenant-a", "retry")
	ctx := context.Background()
	failed := postWorkspacePreparationForTest(handler, ctx, current, "failed-prepare", "req-failed", 1)
	if failed.Code != http.StatusServiceUnavailable || strings.Contains(failed.Body.String(), "private Git diagnostic") {
		t.Fatalf("expected a redacted 503: %d: %s", failed.Code, failed.Body.String())
	}
	restored := getPreparationWorkspaceForTest(t, handler, current)
	if restored.State != workspace.StateRegistered || restored.Version != 3 || restored.Path != "" {
		t.Fatalf("expected retryable REGISTERED version 3, got %#v", restored)
	}
	assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, current, "failed-prepare", "req-old-replay", 1), "version_conflict")
	assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, current, "failed-prepare", "req-rebind-key", 3), "idempotency_conflict")
	assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, current, "retry-prepare", "req-stale-version", 1), "version_conflict")
	if calls.Load() != 1 || len(preparationEventsForTest(t, handler, current)) != 5 {
		t.Fatal("rejected retries must not call the preparer or append events")
	}
	// 获取新版本后用新 key 重试；先前因版本错误被拒绝的 key 尚未占用。
	retry := postWorkspacePreparationForTest(handler, ctx, current, "retry-prepare", "req-retry", restored.Version)
	if retry.Code != http.StatusOK {
		t.Fatalf("fresh retry failed: %d: %s", retry.Code, retry.Body.String())
	}
	ready := getPreparationWorkspaceForTest(t, handler, current)
	if ready.State != workspace.StateReady || ready.Version != 5 || ready.Path == "" {
		t.Fatalf("expected READY version 5 after one failed and one successful attempt, got %#v", ready)
	}
	replay := postWorkspacePreparationForTest(handler, ctx, current, "retry-prepare", "req-retry-replay", restored.Version)
	if replay.Code != http.StatusOK || replay.Body.String() != retry.Body.String() {
		t.Fatalf("successful retry must be replayable: %d: %s", replay.Code, replay.Body.String())
	}
	events := preparationEventsForTest(t, handler, current)
	if calls.Load() != 2 || len(events) != 7 {
		t.Fatal("expected exactly two preparations and seven events through retry")
	}
	for index, want := range []struct {
		eventType task.EventType
		state     workspace.State
		version   uint64
		requestID string
	}{
		{task.EventTypeWorkspacePreparing, workspace.StatePreparing, 2, "req-failed"},
		{task.EventTypeWorkspacePreparationFailed, workspace.StateRegistered, 3, "req-failed"},
		{task.EventTypeWorkspacePreparing, workspace.StatePreparing, 4, "req-retry"},
		{task.EventTypeWorkspaceReady, workspace.StateReady, 5, "req-retry"},
	} {
		event := events[index+3]
		payload := event.Payload.Workspace
		if event.EventType != want.eventType || event.Sequence != uint64(index+4) || event.CausationID != want.requestID || payload == nil || payload.WorkspaceID != current.ID || payload.State != string(want.state) || payload.Version != want.version {
			t.Fatalf("unexpected preparation event: %#v", event)
		}
	}
}
