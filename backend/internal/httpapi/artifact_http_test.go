package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestArchiveReadyWorkspaceDiffAsArtifact(t *testing.T) {
	patch := "diff --git a/README.md b/README.md\n-base\n+head\n"
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
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
	prepareWorkspaceForTest(t, handler, "prepare-for-artifact")

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/artifacts/diff",
		strings.NewReader(`{
			"requestId":"req-archive-diff-1",
			"idempotencyKey":"archive-diff-1",
			"tenantId":"tenant-a",
			"expectedWorkspaceVersion":3
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, response.Code, response.Body.String())
	}
	var body struct {
		ID          string `json:"id"`
		TenantID    string `json:"tenantId"`
		TaskID      string `json:"taskId"`
		WorkspaceID string `json:"workspaceId"`
		Type        string `json:"type"`
		MediaType   string `json:"mediaType"`
		SHA256      string `json:"sha256"`
		SizeBytes   int64  `json:"sizeBytes"`
		CreatedAt   string `json:"createdAt"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Artifact response: %v", err)
	}
	if body.ID != "artifact-1" || body.TenantID != "tenant-a" || body.TaskID != "task-1" || body.WorkspaceID != "workspace-1" {
		t.Fatalf("unexpected Artifact identity: %#v", body)
	}
	if body.Type != "REPOSITORY_DIFF" || body.MediaType != "text/x-diff" || body.SizeBytes != int64(len(patch)) || len(body.SHA256) != 64 || body.CreatedAt == "" {
		t.Fatalf("unexpected Artifact metadata: %#v", body)
	}
	if location := response.Header().Get("Location"); location != "/api/v1/artifacts/artifact-1" {
		t.Fatalf("expected Artifact Location, got %q", location)
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID != "req-archive-diff-1" {
		t.Fatalf("expected request ID, got %q", requestID)
	}

	getRequest := httptest.NewRequest(
		http.MethodGet,
		response.Header().Get("Location")+"?tenantId=tenant-a",
		nil,
	)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("expected Artifact GET status %d, got %d: %s", http.StatusOK, getResponse.Code, getResponse.Body.String())
	}
	var found struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&found); err != nil {
		t.Fatalf("decode found Artifact: %v", err)
	}
	if found.ID != body.ID || found.SHA256 != body.SHA256 {
		t.Fatalf("expected Artifact %#v, got %#v", body, found)
	}

	contentRequest := httptest.NewRequest(
		http.MethodGet,
		response.Header().Get("Location")+"/content?tenantId=tenant-a",
		nil,
	)
	contentResponse := httptest.NewRecorder()
	handler.ServeHTTP(contentResponse, contentRequest)
	if contentResponse.Code != http.StatusOK {
		t.Fatalf("expected Artifact content status %d, got %d: %s", http.StatusOK, contentResponse.Code, contentResponse.Body.String())
	}
	if contentType := contentResponse.Header().Get("Content-Type"); contentType != "text/x-diff" {
		t.Fatalf("expected text/x-diff, got %q", contentType)
	}
	if etag := contentResponse.Header().Get("ETag"); etag != `"`+body.SHA256+`"` {
		t.Fatalf("expected checksum ETag, got %q", etag)
	}
	if contentResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff on Artifact content")
	}
	if contentResponse.Body.String() != patch {
		t.Fatalf("expected archived patch %q, got %q", patch, contentResponse.Body.String())
	}

	replayRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/artifacts/diff",
		strings.NewReader(`{
			"requestId":"req-archive-diff-1-replay",
			"idempotencyKey":"archive-diff-1",
			"tenantId":"tenant-a",
			"expectedWorkspaceVersion":3
		}`),
	)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("expected replay status %d, got %d: %s", http.StatusOK, replayResponse.Code, replayResponse.Body.String())
	}
	var replayed struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(replayResponse.Body).Decode(&replayed); err != nil {
		t.Fatalf("decode replayed Artifact: %v", err)
	}
	if replayed.ID != body.ID {
		t.Fatalf("expected replayed Artifact %q, got %q", body.ID, replayed.ID)
	}
}
