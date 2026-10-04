package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/task"
)

func TestGetTask(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"?tenantId=tenant-a", nil)
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

func TestGetTaskHidesOtherTenants(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{"requestId":"req-get-tenant-a","idempotencyKey":"get-tenant-a","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review A",`+testRepositoryJSON+`}`),
	))
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create task: status %d: %s", createResponse.Code, createResponse.Body.String())
	}

	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1?tenantId=tenant-b", nil))
	if foreignResponse.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another tenant, got %d: %s", foreignResponse.Code, foreignResponse.Body.String())
	}

	ownerResponse := httptest.NewRecorder()
	handler.ServeHTTP(ownerResponse, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1?tenantId=tenant-a", nil))
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("expected owner to read task, got %d: %s", ownerResponse.Code, ownerResponse.Body.String())
	}
}

func TestListTasksReturnsNewestFirst(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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

	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?tenantId=tenant-a", nil)
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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?tenantId=tenant-a", nil)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

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

func TestListTasksHidesOtherTenants(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	for _, body := range []string{
		`{"requestId":"req-tenant-a","idempotencyKey":"create-a","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review A",` + testRepositoryJSON + `}`,
		`{"requestId":"req-tenant-b","idempotencyKey":"create-b","tenantId":"tenant-b","type":"PR_REVIEW","goal":"Review B",` + testRepositoryJSON + `}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))
		if response.Code != http.StatusCreated {
			t.Fatalf("create task: status %d: %s", response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?tenantId=tenant-a", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list tasks: status %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []task.Task `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].TenantID != "tenant-a" || body.Items[0].Goal != "Review A" {
		t.Fatalf("expected only tenant-a task, got %#v", body.Items)
	}
}

func TestTaskReadsRequireTenantID(t *testing.T) {
	for _, path := range []string{"/api/v1/tasks", "/api/v1/tasks/task-1"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for missing tenantId, got %d: %s", response.Code, response.Body.String())
			}
			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if body.Error != "validation_error" || body.Message != "tenantId is required" {
				t.Fatalf("unexpected error: %#v", body)
			}
		})
	}
}

func TestGetTaskReturnsNotFound(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-missing?tenantId=tenant-a", nil)
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
