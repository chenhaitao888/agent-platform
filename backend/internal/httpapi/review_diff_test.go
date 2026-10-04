package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestGetDiffFromAReadyWorkspace(t *testing.T) {
	var gotPath, gotBaseSHA, gotHeadSHA string
	patch := "diff --git a/README.md b/README.md\n-base\n+head\n"
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(_ context.Context, input repository.DiffInput) ([]byte, error) {
			gotPath = input.WorktreePath
			gotBaseSHA = input.BaseSHA
			gotHeadSHA = input.HeadSHA
			return []byte(patch), nil
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	prepareRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace/prepare",
		strings.NewReader(`{
			"requestId":"req-prepare-for-diff",
			"idempotencyKey":"prepare-for-diff",
			"tenantId":"tenant-a",
			"expectedVersion":1
		}`),
	)
	prepareResponse := httptest.NewRecorder()
	handler.ServeHTTP(prepareResponse, prepareRequest)
	if prepareResponse.Code != http.StatusOK {
		t.Fatalf("expected prepare status %d, got %d: %s", http.StatusOK, prepareResponse.Code, prepareResponse.Body.String())
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-a",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	var body struct {
		TaskID      string `json:"taskId"`
		WorkspaceID string `json:"workspaceId"`
		BaseSHA     string `json:"baseSha"`
		HeadSHA     string `json:"headSha"`
		MediaType   string `json:"mediaType"`
		SHA256      string `json:"sha256"`
		SizeBytes   int64  `json:"sizeBytes"`
		Patch       string `json:"patch"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode diff response: %v", err)
	}
	if body.TaskID != "task-1" || body.WorkspaceID != "workspace-1" || body.MediaType != "text/x-diff" {
		t.Fatalf("unexpected diff identity: %#v", body)
	}
	if body.Patch != patch || body.SizeBytes != int64(len(patch)) || len(body.SHA256) != 64 {
		t.Fatalf("unexpected diff payload metadata: %#v", body)
	}
	if !strings.HasSuffix(gotPath, "/workspace-1/worktree") {
		t.Fatalf("expected generated worktree path, got %q", gotPath)
	}
	if gotBaseSHA != testBaseSHA || gotHeadSHA != testHeadSHA {
		t.Fatalf("expected fixed Task SHAs, got base=%q head=%q", gotBaseSHA, gotHeadSHA)
	}
}

func TestGetWorkspaceDiffReturnsAStableOversizedError(t *testing.T) {
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
			return nil, fmt.Errorf("%w: internal Git output", repository.ErrDiffTooLarge)
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	prepareRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace/prepare",
		strings.NewReader(`{
			"requestId":"req-prepare-for-large-diff",
			"idempotencyKey":"prepare-for-large-diff",
			"tenantId":"tenant-a",
			"expectedVersion":1
		}`),
	)
	prepareResponse := httptest.NewRecorder()
	handler.ServeHTTP(prepareResponse, prepareRequest)
	if prepareResponse.Code != http.StatusOK {
		t.Fatalf("expected prepare status %d, got %d: %s", http.StatusOK, prepareResponse.Code, prepareResponse.Body.String())
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-a",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d: %s", http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "diff_too_large" || body.Message != "repository diff exceeds the supported size" {
		t.Fatalf("unexpected stable error: %#v", body)
	}
}

func TestGetWorkspaceDiffRequiresAReadyWorkspace(t *testing.T) {
	diffCalls := 0
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
			diffCalls++
			return []byte("must not be read"), nil
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
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-a",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d: %s", http.StatusConflict, response.Code, response.Body.String())
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "workspace_not_ready" {
		t.Fatalf("expected workspace_not_ready, got %#v", body)
	}
	if diffCalls != 0 {
		t.Fatalf("expected Git not to run before READY, got %d calls", diffCalls)
	}
}
