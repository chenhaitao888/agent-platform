package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateTaskRejectsBlankRequiredFields(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
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

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "repository provider, repositoryId and headSha are required" {
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
				"repositoryId":"platform/project-7",
				"baseSha":"1111111111111111111111111111111111111111",
				"headSha":"2222222222222222222222222222222222222222"
			}
		}`),
	)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

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

func TestCreateTaskValidatesGitLabRepositoryID(t *testing.T) {
	for _, test := range []struct {
		name         string
		repositoryID string
		wantStatus   int
	}{
		{"numeric project ID", "42", http.StatusCreated},
		{"namespaced path", "platform/project-7", http.StatusCreated},
		{"nested namespace and allowed punctuation", "team/sub_team/repo-1.2", http.StatusCreated},
		{"maximum length", "team/" + strings.Repeat("a", maxIdentifierBytes-len("team/")), http.StatusCreated},
		{"single slug", "project-7", http.StatusBadRequest},
		{"parent path", "..", http.StatusBadRequest},
		{"embedded parent path", "team/../project", http.StatusBadRequest},
		{"double dots within a segment", "team/pro..ject", http.StatusBadRequest},
		{"empty path segment", "team//project", http.StatusBadRequest},
		{"leading slash", "/team/project", http.StatusBadRequest},
		{"trailing slash", "team/project/", http.StatusBadRequest},
		{"current path segment", "team/./project", http.StatusBadRequest},
		{"query characters", "team/project?x=1", http.StatusBadRequest},
		{"pre-encoded slash", "team%2Fproject", http.StatusBadRequest},
		{"non-ASCII characters", "团队/project", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"requestId":"req-repository-id","idempotencyKey":"repository-id","tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review pull request", "repository":{"provider":"gitlab","repositoryId":%q,"baseSha":%q,"headSha":%q}}`, test.repositoryID, testBaseSHA, testHeadSHA)
			response := httptest.NewRecorder()
			newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))
			if response.Code != test.wantStatus {
				t.Fatalf("repositoryId %q: expected status %d, got %d: %s", test.repositoryID, test.wantStatus, response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusBadRequest {
				var result errorResponse
				if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if result.Error != "validation_error" || result.Message != "repositoryId must be a numeric project ID or a namespace/path" {
					t.Fatalf("unexpected error response: %#v", result)
				}
			}
		})
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
				"repositoryId":"platform/project-7",
				"baseSha":"main",
				"headSha":"2222222222222222222222222222222222222222"
			}
		}`),
	)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "headSha and any legacy baseSha must be 40 or 64 hexadecimal characters" {
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

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

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
	handler := newHandlerWithVerifiedRepositories()
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

func TestCreateTaskRejectsBodyLargerThan64KiB(t *testing.T) {
	body := `{"requestId":"req-large-body","idempotencyKey":"large-body","tenantId":"tenant-a","type":"PR_REVIEW","goal":"` + strings.Repeat("x", 64<<10) + `",` + testRepositoryJSON + `}`
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized request body, got %d", response.Code)
	}
	var result errorResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if result.Error != "validation_error" {
		t.Fatalf("expected validation_error, got %#v", result)
	}
}

func TestCreateTaskRejectsGoalLargerThan4KiB(t *testing.T) {
	body := `{"requestId":"req-large-goal","idempotencyKey":"large-goal","tenantId":"tenant-a","type":"PR_REVIEW","goal":"` + strings.Repeat("x", (4<<10)+1) + `",` + testRepositoryJSON + `}`
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized goal, got %d", response.Code)
	}
	var result errorResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if result.Error != "validation_error" {
		t.Fatalf("expected validation_error, got %#v", result)
	}
}

func TestCreateTaskAcceptsGoalAt4KiBBoundary(t *testing.T) {
	body := `{"requestId":"req-goal-boundary","idempotencyKey":"goal-boundary","tenantId":"tenant-a","type":"PR_REVIEW","goal":"` + strings.Repeat("x", 4<<10) + `",` + testRepositoryJSON + `}`
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a 4 KiB goal, got %d", response.Code)
	}
}

func TestCreateTaskCountsMultibyteGoalLengthInBytes(t *testing.T) {
	boundaryGoal := strings.Repeat("中", 1365) + "x" // 1365×3 + 1 = 4096 字节。
	for _, test := range []struct {
		name      string
		goal      string
		status    int
		taskCount int
	}{
		{"exactly 4 KiB", boundaryGoal, http.StatusCreated, 1},
		{"one byte above 4 KiB", boundaryGoal + "x", http.StatusBadRequest, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newHandlerWithVerifiedRepositories()
			body := fmt.Sprintf(`{
				"requestId":"req-multibyte-goal","idempotencyKey":"multibyte-goal",
				"tenantId":"tenant-a","type":"PR_REVIEW","goal":%q,%s
			}`, test.goal, testRepositoryJSON)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))
			if response.Code != test.status {
				t.Fatalf("expected status %d for %d goal bytes, got %d: %s", test.status, len(test.goal), response.Code, response.Body.String())
			}
			if test.status == http.StatusBadRequest && !strings.Contains(response.Body.String(), `"error":"validation_error"`) {
				t.Fatalf("expected validation_error, got %s", response.Body.String())
			}
			list := httptest.NewRecorder()
			handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?tenantId=tenant-a", nil))
			var tasks struct {
				Items []struct {
					Goal string `json:"goal"`
				} `json:"items"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &tasks); err != nil {
				t.Fatalf("decode Task list: %v", err)
			}
			if list.Code != http.StatusOK || len(tasks.Items) != test.taskCount {
				t.Fatalf("expected %d stored Tasks, got %d: %s", test.taskCount, list.Code, list.Body.String())
			}
			if test.taskCount == 1 && tasks.Items[0].Goal != test.goal {
				t.Fatal("the accepted multibyte goal must be stored without truncation")
			}
		})
	}
}

func TestWriteRoutesRejectOversizedTrailingWhitespace(t *testing.T) {
	for _, route := range []struct {
		name   string
		method string
		path   string
	}{
		{"create task", http.MethodPost, "/api/v1/tasks"},
		{"update task", http.MethodPatch, "/api/v1/tasks/task-1"},
		{"register workspace", http.MethodPost, "/api/v1/tasks/task-1/workspace"},
		{"prepare workspace", http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare"},
		{"archive diff", http.MethodPost, "/api/v1/tasks/task-1/artifacts/diff"},
	} {
		t.Run(route.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			body := `{}` + strings.Repeat(" ", maxRequestBodyBytes)
			newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for oversized body, got %d", response.Code)
			}
			var result errorResponse
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if result.Error != "validation_error" || result.Message != "request body must not exceed 64 KiB" {
				t.Fatalf("expected body size error, got %#v", result)
			}
		})
	}
}

func TestCreateTaskRejectsOversizedIdentifiers(t *testing.T) {
	validBody := `{"requestId":"req-1","idempotencyKey":"key-1","tenantId":"tenant-a","type":"PR_REVIEW","goal":"review",` + testRepositoryJSON + `}`
	for _, field := range []struct {
		name  string
		value string
	}{
		{"requestId", `"requestId":"req-1"`},
		{"idempotencyKey", `"idempotencyKey":"key-1"`},
		{"tenantId", `"tenantId":"tenant-a"`},
		{"type", `"type":"PR_REVIEW"`},
		{"repositoryId", `"repositoryId":"platform/project-7"`},
	} {
		t.Run(field.name, func(t *testing.T) {
			longField := `"` + field.name + `":"` + strings.Repeat("x", maxIdentifierBytes+1) + `"`
			body := strings.Replace(validBody, field.value, longField, 1)
			response := httptest.NewRecorder()
			newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for oversized %s, got %d", field.name, response.Code)
			}
			var result errorResponse
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if result.Error != "validation_error" || result.Message != "request fields exceed supported length" {
				t.Fatalf("expected field length error, got %#v", result)
			}
		})
	}
}

func TestOtherWriteRoutesRejectOversizedIdempotencyKey(t *testing.T) {
	longKey := strings.Repeat("x", maxIdentifierBytes+1)
	for _, route := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"update task", http.MethodPatch, "/api/v1/tasks/task-1", `{"requestId":"req-1","idempotencyKey":"` + longKey + `","tenantId":"tenant-a","expectedVersion":1,"status":"QUEUED"}`},
		{"register workspace", http.MethodPost, "/api/v1/tasks/task-1/workspace", `{"requestId":"req-1","idempotencyKey":"` + longKey + `","tenantId":"tenant-a"}`},
		{"prepare workspace", http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", `{"requestId":"req-1","idempotencyKey":"` + longKey + `","tenantId":"tenant-a","expectedVersion":1}`},
		{"archive diff", http.MethodPost, "/api/v1/tasks/task-1/artifacts/diff", `{"requestId":"req-1","idempotencyKey":"` + longKey + `","tenantId":"tenant-a","expectedWorkspaceVersion":1}`},
	} {
		t.Run(route.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			newHandlerWithVerifiedRepositories().ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for oversized idempotencyKey, got %d", response.Code)
			}
			var result errorResponse
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if result.Error != "validation_error" || result.Message != "request fields exceed supported length" {
				t.Fatalf("expected field length error, got %#v", result)
			}
		})
	}
}
