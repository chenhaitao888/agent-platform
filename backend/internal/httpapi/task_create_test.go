package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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
				"repositoryId":"platform/project-7",
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
	if body.Repository.Provider != "gitlab" || body.Repository.RepositoryID != "platform/project-7" {
		t.Errorf("expected gitlab/platform/project-7 repository, got %#v", body.Repository)
	}
	if body.Repository.BaseSHA != "1111111111111111111111111111111111111111" || body.Repository.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("expected immutable base/head SHA, got %#v", body.Repository)
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID != "req-create-1" {
		t.Errorf("expected X-Request-ID req-create-1, got %q", requestID)
	}

	expectedLocation := "/api/v1/tasks/" + body.ID + "?tenantId=tenant-a"
	if location := response.Header().Get("Location"); location != expectedLocation {
		t.Errorf("expected Location %q, got %q", expectedLocation, location)
	}
}

func TestCreateTaskReplaysTheSameIdempotentRequest(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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
	if location := secondResponse.Header().Get("Location"); location != "/api/v1/tasks/"+firstTask.ID+"?tenantId=tenant-a" {
		t.Fatalf("expected replay Location for %q, got %q", firstTask.ID, location)
	}
}

func TestCreateTaskRejectsAnIdempotencyKeyWithDifferentContent(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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
	handler := newHandlerWithVerifiedRepositories()
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
				"repositoryId":"platform/project-7",
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
	handler := newHandlerWithVerifiedRepositories()
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
