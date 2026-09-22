package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		strings.NewReader(`{"requestId":"req-create-1","idempotencyKey":"review-42","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review pull request 42"}`),
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
		"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review a different pull request"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
		strings.NewReader(`{"requestId":"req-get-1","idempotencyKey":"bug-fix-checkout","tenantId":"tenant-a","type":"BUG_FIX","goal":"Fix checkout timeout"}`),
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
		`{"requestId":"req-list-1","idempotencyKey":"review-42","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review pull request 42"}`,
		`{"requestId":"req-list-2","idempotencyKey":"bug-fix-checkout","tenantId":"tenant-a","type":"BUG_FIX","goal":"Fix checkout timeout"}`,
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
			"goal":"Review pull request 42"
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
