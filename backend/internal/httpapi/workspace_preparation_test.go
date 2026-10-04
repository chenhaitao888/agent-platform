package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/task"
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
