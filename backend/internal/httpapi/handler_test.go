package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

const (
	testBaseSHA = "1111111111111111111111111111111111111111"
	testHeadSHA = "2222222222222222222222222222222222222222"

	testRepositoryJSON = `"repository":{
	"provider":"gitlab",
	"repositoryId":"project-7",
	"baseSha":"1111111111111111111111111111111111111111",
	"headSha":"2222222222222222222222222222222222222222"
}`
)

func TestHealthz(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("expected status ok, got %q", body.Status)
	}
	if body.Service != "agent-platform-api" {
		t.Errorf("expected service agent-platform-api, got %q", body.Service)
	}
}

func TestHealthzRejectsNonGetRequests(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow header %q, got %q", http.MethodGet, allow)
	}
}

func TestCreateTask(t *testing.T) {
	handler := NewHandler()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-1",
			"idempotencyKey":"review-42",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			"repository":{
				"provider":"gitlab",
				"repositoryId":"project-7",
				"baseSha":"1111111111111111111111111111111111111111",
				"headSha":"2222222222222222222222222222222222222222"
			}
		}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, response.Code)
	}

	var body struct {
		ID             string `json:"id"`
		TenantID       string `json:"tenantId"`
		IdempotencyKey string `json:"idempotencyKey"`
		Type           string `json:"type"`
		Goal           string `json:"goal"`
		Status         string `json:"status"`
		Version        uint64 `json:"version"`
		CreatedAt      string `json:"createdAt"`
		Repository     struct {
			Provider     string `json:"provider"`
			RepositoryID string `json:"repositoryId"`
			BaseSHA      string `json:"baseSha"`
			HeadSHA      string `json:"headSha"`
		} `json:"repository"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.ID == "" {
		t.Error("expected generated task ID")
	}
	if body.Type != "PR_REVIEW" {
		t.Errorf("expected type PR_REVIEW, got %q", body.Type)
	}
	if body.TenantID != "tenant-a" {
		t.Errorf("expected tenant tenant-a, got %q", body.TenantID)
	}
	if body.IdempotencyKey != "review-42" {
		t.Errorf("expected idempotency key review-42, got %q", body.IdempotencyKey)
	}
	if body.Goal != "Review pull request 42" {
		t.Errorf("expected goal to be preserved, got %q", body.Goal)
	}
	if body.Status != "CREATED" {
		t.Errorf("expected status CREATED, got %q", body.Status)
	}
	if body.CreatedAt == "" {
		t.Error("expected creation time")
	}
	if body.Version != 1 {
		t.Errorf("expected version 1, got %d", body.Version)
	}
	if body.Repository.Provider != "gitlab" || body.Repository.RepositoryID != "project-7" {
		t.Errorf("expected gitlab/project-7 repository, got %#v", body.Repository)
	}
	if body.Repository.BaseSHA != "1111111111111111111111111111111111111111" || body.Repository.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("expected immutable base/head SHA, got %#v", body.Repository)
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID != "req-create-1" {
		t.Errorf("expected X-Request-ID req-create-1, got %q", requestID)
	}

	expectedLocation := "/api/v1/tasks/" + body.ID
	if location := response.Header().Get("Location"); location != expectedLocation {
		t.Errorf("expected Location %q, got %q", expectedLocation, location)
	}
}

func TestCreateTaskReplaysTheSameIdempotentRequest(t *testing.T) {
	handler := NewHandler()
	body := `{
		"requestId":"req-1",
		"idempotencyKey":"review:project-7:mr-42:head-a13f",
		"tenantId":"tenant-a",
		"type":"PR_REVIEW",
		"goal":"Review pull request 42",
		` + testRepositoryJSON + `
	}`

	firstRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(body),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstRequest)

	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("expected first status %d, got %d", http.StatusCreated, firstResponse.Code)
	}

	var firstTask struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(firstResponse.Body).Decode(&firstTask); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	secondRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(body),
	)
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, secondRequest)

	if secondResponse.Code != http.StatusOK {
		t.Fatalf("expected replay status %d, got %d", http.StatusOK, secondResponse.Code)
	}

	var replayedTask struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(secondResponse.Body).Decode(&replayedTask); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayedTask.ID != firstTask.ID {
		t.Fatalf("expected replayed task %q, got %q", firstTask.ID, replayedTask.ID)
	}
	if location := secondResponse.Header().Get("Location"); location != "/api/v1/tasks/"+firstTask.ID {
		t.Fatalf("expected replay Location for %q, got %q", firstTask.ID, location)
	}
}

func TestCreateTaskRejectsAnIdempotencyKeyWithDifferentContent(t *testing.T) {
	handler := NewHandler()
	firstRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-conflict-1",
			"idempotencyKey":"review:project-7:mr-42:head-a13f",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstRequest)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("expected first status %d, got %d", http.StatusCreated, firstResponse.Code)
	}

	conflictingRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-conflict-2",
			"idempotencyKey":"review:project-7:mr-42:head-a13f",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review a different pull request",
			`+testRepositoryJSON+`
		}`),
	)
	conflictingResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictingResponse, conflictingRequest)

	if conflictingResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, conflictingResponse.Code)
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(conflictingResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "idempotency_conflict" {
		t.Errorf("expected idempotency_conflict, got %q", body.Error)
	}
	if requestID := conflictingResponse.Header().Get("X-Request-ID"); requestID != "req-conflict-2" {
		t.Errorf("expected X-Request-ID req-conflict-2, got %q", requestID)
	}
}

func TestCreateTaskRejectsAnIdempotencyKeyWithDifferentRepositoryReference(t *testing.T) {
	handler := NewHandler()
	firstRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-repository-conflict-1",
			"idempotencyKey":"repository-conflict",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstRequest)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("expected first status %d, got %d", http.StatusCreated, firstResponse.Code)
	}

	conflictingRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-repository-conflict-2",
			"idempotencyKey":"repository-conflict",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			"repository":{
				"provider":"gitlab",
				"repositoryId":"project-7",
				"baseSha":"1111111111111111111111111111111111111111",
				"headSha":"3333333333333333333333333333333333333333"
			}
		}`),
	)
	conflictingResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictingResponse, conflictingRequest)

	if conflictingResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, conflictingResponse.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(conflictingResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "idempotency_conflict" {
		t.Errorf("expected idempotency_conflict, got %q", body.Error)
	}
}

func TestCreateTaskScopesIdempotencyKeysByTenant(t *testing.T) {
	handler := NewHandler()
	firstRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-tenant-a",
			"idempotencyKey":"review:project-7:mr-42:head-a13f",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, firstRequest)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("expected first status %d, got %d", http.StatusCreated, firstResponse.Code)
	}

	secondRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-tenant-b",
			"idempotencyKey":"review:project-7:mr-42:head-a13f",
			"tenantId":"tenant-b",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, secondRequest)

	if secondResponse.Code != http.StatusCreated {
		t.Fatalf("expected second tenant status %d, got %d", http.StatusCreated, secondResponse.Code)
	}

	var firstTask, secondTask struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
	}
	if err := json.NewDecoder(firstResponse.Body).Decode(&firstTask); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if err := json.NewDecoder(secondResponse.Body).Decode(&secondTask); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if firstTask.ID == secondTask.ID {
		t.Fatalf("expected separate tasks, both got %q", firstTask.ID)
	}
	if firstTask.TenantID != "tenant-a" || secondTask.TenantID != "tenant-b" {
		t.Fatalf("unexpected tenants: %#v and %#v", firstTask, secondTask)
	}
}

func TestGetTask(t *testing.T) {
	handler := NewHandler()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{"requestId":"req-get-1","idempotencyKey":"bug-fix-checkout","tenantId":"tenant-a","type":"BUG_FIX","goal":"Fix checkout timeout",`+testRepositoryJSON+`}`),
	)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)

	if getResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getResponse.Code)
	}

	var found struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Goal   string `json:"goal"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&found); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	if found.ID != created.ID {
		t.Errorf("expected ID %q, got %q", created.ID, found.ID)
	}
	if found.Type != "BUG_FIX" {
		t.Errorf("expected type BUG_FIX, got %q", found.Type)
	}
	if found.Goal != "Fix checkout timeout" {
		t.Errorf("expected goal to be preserved, got %q", found.Goal)
	}
	if found.Status != "CREATED" {
		t.Errorf("expected status CREATED, got %q", found.Status)
	}
}

func TestUpdateTaskQueuesACreatedTask(t *testing.T) {
	handler := NewHandler()
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

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1", nil)
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
	handler := NewHandler()
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
	handler := NewHandler()
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
	handler := NewHandler()
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
	handler := NewHandler()
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

func TestListTasksReturnsNewestFirst(t *testing.T) {
	handler := NewHandler()
	requestBodies := []string{
		`{"requestId":"req-list-1","idempotencyKey":"review-42","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review pull request 42",` + testRepositoryJSON + `}`,
		`{"requestId":"req-list-2","idempotencyKey":"bug-fix-checkout","tenantId":"tenant-a","type":"BUG_FIX","goal":"Fix checkout timeout",` + testRepositoryJSON + `}`,
	}
	for _, body := range requestBodies {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/tasks",
			strings.NewReader(body),
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("expected create status %d, got %d", http.StatusCreated, response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var body struct {
		Items []struct {
			ID   string `json:"id"`
			Goal string `json:"goal"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.Items) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(body.Items))
	}
	if body.Items[0].ID != "task-2" || body.Items[0].Goal != "Fix checkout timeout" {
		t.Errorf("expected newest task first, got %#v", body.Items[0])
	}
	if body.Items[1].ID != "task-1" || body.Items[1].Goal != "Review pull request 42" {
		t.Errorf("expected oldest task second, got %#v", body.Items[1])
	}
}

func TestListTasksReturnsAnEmptyArray(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Items == nil {
		t.Fatal("expected items to be an empty array, got null")
	}
	if len(body.Items) != 0 {
		t.Fatalf("expected no tasks, got %d", len(body.Items))
	}
}

func TestCreateTaskRejectsBlankRequiredFields(t *testing.T) {
	handler := NewHandler()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{"requestId":"req-blank-1","idempotencyKey":"blank-goal","tenantId":"tenant-a","type":"PR_REVIEW","goal":"   "}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Error != "validation_error" {
		t.Errorf("expected validation_error, got %q", body.Error)
	}
	if body.Message != "type and goal are required" {
		t.Errorf("unexpected error message %q", body.Message)
	}
}

func TestCreateTaskRequiresRepositoryReference(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-missing-repository",
			"idempotencyKey":"missing-repository",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42"
		}`),
	)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "repository provider, repositoryId, baseSha and headSha are required" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateTaskRejectsAnUnsupportedRepositoryProvider(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-unsupported-provider",
			"idempotencyKey":"unsupported-provider",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			"repository":{
				"provider":"github",
				"repositoryId":"project-7",
				"baseSha":"1111111111111111111111111111111111111111",
				"headSha":"2222222222222222222222222222222222222222"
			}
		}`),
	)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "repository provider must be gitlab" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateTaskRejectsBranchNamesAsRepositorySHA(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-branch-name",
			"idempotencyKey":"branch-name",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			"repository":{
				"provider":"gitlab",
				"repositoryId":"project-7",
				"baseSha":"main",
				"headSha":"2222222222222222222222222222222222222222"
			}
		}`),
	)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "baseSha and headSha must be 40 or 64 hexadecimal characters" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateTaskRequiresRequestAndTenantMetadata(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{"type":"PR_REVIEW","goal":"Review pull request 42"}`),
	)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" {
		t.Errorf("expected validation_error, got %q", body.Error)
	}
	if body.Message != "requestId, idempotencyKey and tenantId are required" {
		t.Errorf("unexpected error message %q", body.Message)
	}
}

func TestCreateTaskRejectsInvalidJSON(t *testing.T) {
	handler := NewHandler()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{"type":`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Error != "invalid_json" {
		t.Errorf("expected invalid_json, got %q", body.Error)
	}
	if body.Message != "request body must be valid JSON" {
		t.Errorf("unexpected error message %q", body.Message)
	}
}

func TestGetTaskReturnsNotFound(t *testing.T) {
	handler := NewHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-missing", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Error != "not_found" {
		t.Errorf("expected not_found, got %q", body.Error)
	}
	if body.Message != "task not found" {
		t.Errorf("unexpected error message %q", body.Message)
	}
}

func TestListTaskEventsReturnsTheCreationEvent(t *testing.T) {
	handler := NewHandler()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-event",
			"idempotencyKey":"create-event",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d", http.StatusCreated, createResponse.Code)
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("expected events status %d, got %d", http.StatusOK, eventsResponse.Code)
	}

	var body struct {
		Items []struct {
			SchemaVersion string `json:"schemaVersion"`
			EventID       string `json:"eventId"`
			EventType     string `json:"eventType"`
			OccurredAt    string `json:"occurredAt"`
			TenantID      string `json:"tenantId"`
			TaskID        string `json:"taskId"`
			Sequence      uint64 `json:"sequence"`
			CorrelationID string `json:"correlationId"`
			CausationID   string `json:"causationId"`
			Payload       struct {
				Status  string `json:"status"`
				Version uint64 `json:"version"`
			} `json:"payload"`
		} `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 event, got %d", len(body.Items))
	}

	created := body.Items[0]
	if created.SchemaVersion != "1.0" || created.EventID != "evt-1" || created.EventType != "task.created" {
		t.Errorf("unexpected event identity: %#v", created)
	}
	if created.OccurredAt == "" {
		t.Error("expected occurredAt to be present")
	}
	if created.TenantID != "tenant-a" || created.TaskID != "task-1" || created.Sequence != 1 {
		t.Errorf("unexpected event scope: %#v", created)
	}
	if created.CorrelationID != "task-1" || created.CausationID != "req-create-event" {
		t.Errorf("unexpected event tracing fields: %#v", created)
	}
	if created.Payload.Status != "CREATED" || created.Payload.Version != 1 {
		t.Errorf("unexpected event payload: %#v", created.Payload)
	}
}

func TestListTaskEventsReturnsTheQueuedEventAfterCreation(t *testing.T) {
	handler := NewHandler()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-before-queue-event",
			"idempotencyKey":"create-before-queue-event",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	updateRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-event",
			"idempotencyKey":"queue-event",
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

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	var body struct {
		Items []struct {
			EventType   string `json:"eventType"`
			Sequence    uint64 `json:"sequence"`
			CausationID string `json:"causationId"`
			Payload     struct {
				Status  string `json:"status"`
				Version uint64 `json:"version"`
			} `json:"payload"`
		} `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 events, got %d", len(body.Items))
	}

	created, queued := body.Items[0], body.Items[1]
	if created.EventType != "task.created" || created.Sequence != 1 {
		t.Errorf("unexpected creation event: %#v", created)
	}
	if queued.EventType != "task.queued" || queued.Sequence != 2 {
		t.Errorf("unexpected queued event: %#v", queued)
	}
	if queued.CausationID != "req-queue-event" {
		t.Errorf("expected queue request as causation, got %q", queued.CausationID)
	}
	if queued.Payload.Status != "QUEUED" || queued.Payload.Version != 2 {
		t.Errorf("unexpected queued payload: %#v", queued.Payload)
	}
}

func TestListTaskEventsDoesNotDuplicateAnIdempotentTransition(t *testing.T) {
	handler := NewHandler()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-before-event-replay",
			"idempotencyKey":"create-before-event-replay",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	for _, requestID := range []string{"req-queue-event-first", "req-queue-event-replay"} {
		updateRequest := httptest.NewRequest(
			http.MethodPatch,
			"/api/v1/tasks/task-1",
			strings.NewReader(`{
				"requestId":"`+requestID+`",
				"idempotencyKey":"queue-event-replay",
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
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected creation and queued events only, got %d events", len(body.Items))
	}
}

func TestListTaskEventsHidesTasksFromOtherTenants(t *testing.T) {
	handler := NewHandler()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-private-events",
			"idempotencyKey":"create-private-events",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-b",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	if eventsResponse.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, eventsResponse.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events error response: %v", err)
	}
	if body.Error != "not_found" || body.Message != "task not found" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestListTaskEventsRequiresTenantID(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events",
		nil,
	)
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode events error response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "tenantId is required" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateWorkspaceForQueuedTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createTaskRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-task-for-workspace",
			"idempotencyKey":"create-task-for-workspace",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createTaskRequest)

	queueTaskRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-task-for-workspace",
			"idempotencyKey":"queue-task-for-workspace",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), queueTaskRequest)

	workspaceRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-create-workspace",
			"idempotencyKey":"workspace:task-1",
			"tenantId":"tenant-a"
		}`),
	)
	workspaceResponse := httptest.NewRecorder()
	handler.ServeHTTP(workspaceResponse, workspaceRequest)

	if workspaceResponse.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, workspaceResponse.Code)
	}
	if location := workspaceResponse.Header().Get("Location"); location != "/api/v1/tasks/task-1/workspace" {
		t.Fatalf("unexpected Location header %q", location)
	}
	if requestID := workspaceResponse.Header().Get("X-Request-ID"); requestID != "req-create-workspace" {
		t.Fatalf("expected request ID req-create-workspace, got %q", requestID)
	}

	var created struct {
		ID         string `json:"id"`
		TenantID   string `json:"tenantId"`
		TaskID     string `json:"taskId"`
		Repository struct {
			Provider     string `json:"provider"`
			RepositoryID string `json:"repositoryId"`
		} `json:"repository"`
		BaseSHA   string `json:"baseSha"`
		HeadSHA   string `json:"headSha"`
		State     string `json:"state"`
		Version   uint64 `json:"version"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.NewDecoder(workspaceResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode workspace response: %v", err)
	}
	if created.ID != "workspace-1" || created.TenantID != "tenant-a" || created.TaskID != "task-1" {
		t.Errorf("unexpected workspace identity: %#v", created)
	}
	if created.Repository.Provider != "gitlab" || created.Repository.RepositoryID != "project-7" {
		t.Errorf("unexpected repository: %#v", created.Repository)
	}
	if created.BaseSHA != "1111111111111111111111111111111111111111" || created.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("unexpected immutable refs: %#v", created)
	}
	if created.State != "REGISTERED" || created.Version != 1 || created.CreatedAt == "" {
		t.Errorf("unexpected workspace state: %#v", created)
	}
}

func TestPrepareRegisteredWorkspace(t *testing.T) {
	workspaceRoot := t.TempDir()
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		workspaceRoot,
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

func TestArchiveReadyWorkspaceDiffAsArtifact(t *testing.T) {
	patch := "diff --git a/README.md b/README.md\n-base\n+head\n"
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
			return []byte(patch), nil
		}),
		t.TempDir(),
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

func TestPrepareWorkspaceFailureReturnsAStableErrorAndRestoresRegistration(t *testing.T) {
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return errors.New("Git output that must stay internal")
		}),
		t.TempDir(),
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

func TestCreateWorkspaceRejectsAnUnknownRepository(t *testing.T) {
	handler := NewHandlerWithRepositoryVerifier(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrRepositoryNotFound
		},
	))
	createQueuedTaskForWorkspaceTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-unknown-repository",
			"idempotencyKey":"unknown-repository",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "repository_not_found" || body.Message != "repository was not found or is not accessible" {
		t.Errorf("unexpected error response: %#v", body)
	}

	getRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace?tenantId=tenant-a",
		nil,
	)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusNotFound {
		t.Fatalf("expected no workspace after failed verification, got %d", getResponse.Code)
	}
}

func TestCreateWorkspaceRejectsAnUnknownBaseCommit(t *testing.T) {
	handler := NewHandlerWithRepositoryVerifier(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrBaseCommitNotFound
		},
	))
	createQueuedTaskForWorkspaceTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-unknown-base",
			"idempotencyKey":"unknown-base",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "base_commit_not_found" || body.Message != "base commit was not found in repository" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateWorkspaceRejectsAnUnknownHeadCommit(t *testing.T) {
	handler := NewHandlerWithRepositoryVerifier(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrHeadCommitNotFound
		},
	))
	createQueuedTaskForWorkspaceTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-unknown-head",
			"idempotencyKey":"unknown-head",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "head_commit_not_found" || body.Message != "head commit was not found in repository" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateWorkspaceFailsClosedWhenRepositoryVerificationIsUnavailable(t *testing.T) {
	handler := NewHandler()
	createQueuedTaskForWorkspaceTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-verification-unavailable",
			"idempotencyKey":"verification-unavailable",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "repository_verification_unavailable" || body.Message != "repository verification is unavailable" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestCreateWorkspaceRejectsATaskThatIsNotQueued(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createTaskRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-unqueued-task",
			"idempotencyKey":"create-unqueued-task",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createTaskRequest)

	workspaceRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-workspace-before-queue",
			"idempotencyKey":"workspace-before-queue",
			"tenantId":"tenant-a"
		}`),
	)
	workspaceResponse := httptest.NewRecorder()
	handler.ServeHTTP(workspaceResponse, workspaceRequest)

	if workspaceResponse.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, workspaceResponse.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(workspaceResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "task_not_queued" {
		t.Errorf("expected task_not_queued, got %q", body.Error)
	}
}

func TestCreateWorkspaceReplaysTheSameIdempotentRequest(t *testing.T) {
	verificationAttempts := 0
	handler := NewHandlerWithRepositoryVerifier(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			verificationAttempts++
			if verificationAttempts > 1 {
				return repository.ErrVerificationUnavailable
			}
			return nil
		},
	))
	createTaskRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-task-for-workspace-replay",
			"idempotencyKey":"create-task-for-workspace-replay",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createTaskRequest)
	queueTaskRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-task-for-workspace-replay",
			"idempotencyKey":"queue-task-for-workspace-replay",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), queueTaskRequest)

	register := func(requestID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/tasks/task-1/workspace",
			strings.NewReader(`{
				"requestId":"`+requestID+`",
				"idempotencyKey":"workspace-replay",
				"tenantId":"tenant-a"
			}`),
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := register("req-workspace-first")
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first status %d, got %d", http.StatusCreated, first.Code)
	}
	replay := register("req-workspace-replay")
	if replay.Code != http.StatusOK {
		t.Fatalf("expected replay status %d, got %d", http.StatusOK, replay.Code)
	}

	var replayed struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	if err := json.NewDecoder(replay.Body).Decode(&replayed); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayed.ID != "workspace-1" || replayed.Version != 1 {
		t.Errorf("unexpected replayed workspace: %#v", replayed)
	}
	if requestID := replay.Header().Get("X-Request-ID"); requestID != "req-workspace-replay" {
		t.Errorf("expected replay request ID, got %q", requestID)
	}
}

func TestGetWorkspaceForTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace?tenantId=tenant-a",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	var found struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
		TaskID   string `json:"taskId"`
		BaseSHA  string `json:"baseSha"`
		HeadSHA  string `json:"headSha"`
		State    string `json:"state"`
		Version  uint64 `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatalf("decode workspace response: %v", err)
	}
	if found.ID != "workspace-1" || found.TenantID != "tenant-a" || found.TaskID != "task-1" {
		t.Errorf("unexpected workspace identity: %#v", found)
	}
	if found.BaseSHA != "1111111111111111111111111111111111111111" || found.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("unexpected immutable refs: %#v", found)
	}
	if found.State != "REGISTERED" || found.Version != 1 {
		t.Errorf("unexpected workspace state: %#v", found)
	}
}

func TestCreateWorkspaceRejectsReusingAKeyForAnotherTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	createSecondTask := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-second-task",
			"idempotencyKey":"create-second-task",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review another pull request",
			`+testRepositoryJSON+`
		}`),
	)
	createSecondResponse := httptest.NewRecorder()
	handler.ServeHTTP(createSecondResponse, createSecondTask)
	if createSecondResponse.Code != http.StatusCreated {
		t.Fatalf("expected second task create status %d, got %d", http.StatusCreated, createSecondResponse.Code)
	}

	queueSecondTask := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-2",
		strings.NewReader(`{
			"requestId":"req-queue-second-task",
			"idempotencyKey":"queue-second-task",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	queueSecondResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueSecondResponse, queueSecondTask)
	if queueSecondResponse.Code != http.StatusOK {
		t.Fatalf("expected second task queue status %d, got %d", http.StatusOK, queueSecondResponse.Code)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-2/workspace",
		strings.NewReader(`{
			"requestId":"req-workspace-idempotency-conflict",
			"idempotencyKey":"register-workspace-for-get",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "idempotency_conflict" {
		t.Errorf("expected idempotency_conflict, got %q", body.Error)
	}
}

func TestCreateWorkspaceRejectsASecondWorkspaceForTheSameTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-second-workspace",
			"idempotencyKey":"second-workspace",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "workspace_already_exists" {
		t.Errorf("expected workspace_already_exists, got %q", body.Error)
	}
}

func TestGetWorkspaceHidesItFromOtherTenants(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/workspace?tenantId=tenant-b",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "not_found" || body.Message != "workspace not found" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func createQueuedTaskForWorkspaceTest(t *testing.T, handler http.Handler) {
	t.Helper()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-task-for-workspace-get",
			"idempotencyKey":"create-task-for-workspace-get",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d", http.StatusCreated, createResponse.Code)
	}

	queueRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-task-for-workspace-get",
			"idempotencyKey":"queue-task-for-workspace-get",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	queueResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueResponse, queueRequest)
	if queueResponse.Code != http.StatusOK {
		t.Fatalf("expected queue status %d, got %d", http.StatusOK, queueResponse.Code)
	}
}

func registerWorkspaceForTest(t *testing.T, handler http.Handler) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace",
		strings.NewReader(`{
			"requestId":"req-register-workspace-for-get",
			"idempotencyKey":"register-workspace-for-get",
			"tenantId":"tenant-a"
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected workspace status %d, got %d", http.StatusCreated, response.Code)
	}
}

func prepareWorkspaceForTest(t *testing.T, handler http.Handler, idempotencyKey string) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/workspace/prepare",
		strings.NewReader(`{
			"requestId":"req-prepare-workspace-for-artifact",
			"idempotencyKey":"`+idempotencyKey+`",
			"tenantId":"tenant-a",
			"expectedVersion":1
		}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected prepare status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
}

type repositoryVerifierFunc func(context.Context, task.RepositoryReference) error

func (verify repositoryVerifierFunc) Verify(ctx context.Context, reference task.RepositoryReference) error {
	return verify(ctx, reference)
}

type workspacePreparerFunc func(context.Context, task.RepositoryReference, string) error

var _ workspace.Preparer = workspacePreparerFunc(nil)

func (prepare workspacePreparerFunc) Prepare(ctx context.Context, reference task.RepositoryReference, destination string) error {
	return prepare(ctx, reference, destination)
}

type diffReaderFunc func(context.Context, repository.DiffInput) ([]byte, error)

func (read diffReaderFunc) Read(ctx context.Context, input repository.DiffInput) ([]byte, error) {
	return read(ctx, input)
}

func newHandlerWithVerifiedRepositories() http.Handler {
	return NewHandlerWithRepositoryVerifier(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return nil
		},
	))
}
