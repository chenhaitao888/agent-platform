package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateTaskQueuesACreatedTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-for-queue",
			"idempotencyKey":"create-for-queue",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)

	updateRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-1",
			"idempotencyKey":"task-1:queue",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updateRequest)

	if updateResponse.Code != http.StatusOK {
		t.Fatalf("expected update status %d, got %d", http.StatusOK, updateResponse.Code)
	}

	var updated struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version uint64 `json:"version"`
	}
	if err := json.NewDecoder(updateResponse.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.ID != "task-1" || updated.Status != "QUEUED" || updated.Version != 2 {
		t.Fatalf("unexpected updated task: %#v", updated)
	}
	if requestID := updateResponse.Header().Get("X-Request-ID"); requestID != "req-queue-1" {
		t.Fatalf("expected X-Request-ID req-queue-1, got %q", requestID)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1?tenantId=tenant-a", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)

	var found struct {
		Status  string `json:"status"`
		Version uint64 `json:"version"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&found); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if found.Status != "QUEUED" || found.Version != 2 {
		t.Fatalf("expected persisted QUEUED version 2, got %#v", found)
	}
}

func TestUpdateTaskRejectsAStaleVersion(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-stale",
			"idempotencyKey":"create-stale",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	firstUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-first",
			"idempotencyKey":"task-1:queue:first",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstUpdate)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("expected first update status %d, got %d", http.StatusOK, firstResponse.Code)
	}

	staleUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-stale",
			"idempotencyKey":"task-1:queue:stale",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleUpdate)

	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, staleResponse.Code)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(staleResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "version_conflict" {
		t.Fatalf("expected version_conflict, got %q", body.Error)
	}
	if requestID := staleResponse.Header().Get("X-Request-ID"); requestID != "req-queue-stale" {
		t.Fatalf("expected stale request ID, got %q", requestID)
	}
}

func TestUpdateTaskReplaysTheSameTransition(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-replay",
			"idempotencyKey":"create-replay",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	firstUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-replay-1",
			"idempotencyKey":"task-1:queue",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstUpdate)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("expected first update status %d, got %d", http.StatusOK, firstResponse.Code)
	}

	replayUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-replay-2",
			"idempotencyKey":"task-1:queue",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replayUpdate)

	if replayResponse.Code != http.StatusOK {
		t.Fatalf("expected replay status %d, got %d", http.StatusOK, replayResponse.Code)
	}

	var replayed struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version uint64 `json:"version"`
	}
	if err := json.NewDecoder(replayResponse.Body).Decode(&replayed); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayed.ID != "task-1" || replayed.Status != "QUEUED" || replayed.Version != 2 {
		t.Fatalf("unexpected replayed task: %#v", replayed)
	}
	if requestID := replayResponse.Header().Get("X-Request-ID"); requestID != "req-queue-replay-2" {
		t.Fatalf("expected replay request ID, got %q", requestID)
	}
}

func TestUpdateTaskRejectsANewTransitionFromQueuedToQueued(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-invalid-transition",
			"idempotencyKey":"create-invalid-transition",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	firstUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-valid",
			"idempotencyKey":"task-1:queue:valid",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstUpdate)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("expected first update status %d, got %d", http.StatusOK, firstResponse.Code)
	}

	invalidUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-invalid",
			"idempotencyKey":"task-1:queue:invalid",
			"tenantId":"tenant-a",
			"expectedVersion":2,
			"status":"QUEUED"
		}`),
	)
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidUpdate)

	if invalidResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, invalidResponse.Code)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(invalidResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "invalid_transition" {
		t.Fatalf("expected invalid_transition, got %q", body.Error)
	}
}

func TestUpdateTaskRejectsReusingAKeyForDifferentTransitionContent(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-update-conflict",
			"idempotencyKey":"create-update-conflict",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	firstUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-update-conflict-1",
			"idempotencyKey":"task-1:queue:shared",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstUpdate)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("expected first update status %d, got %d", http.StatusOK, firstResponse.Code)
	}

	conflictingUpdate := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-update-conflict-2",
			"idempotencyKey":"task-1:queue:shared",
			"tenantId":"tenant-a",
			"expectedVersion":2,
			"status":"QUEUED"
		}`),
	)
	conflictingResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictingResponse, conflictingUpdate)

	if conflictingResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, conflictingResponse.Code)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(conflictingResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "idempotency_conflict" {
		t.Fatalf("expected idempotency_conflict, got %q", body.Error)
	}
}
