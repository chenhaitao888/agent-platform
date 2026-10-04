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
		TargetBranch string `json:"targetBranch"`
		TargetSHA    string `json:"targetSha"`
		BaseSHA      string `json:"baseSha"`
		HeadSHA      string `json:"headSha"`
		State        string `json:"state"`
		Version      uint64 `json:"version"`
		CreatedAt    string `json:"createdAt"`
	}
	if err := json.NewDecoder(workspaceResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode workspace response: %v", err)
	}
	if created.ID != "workspace-1" || created.TenantID != "tenant-a" || created.TaskID != "task-1" {
		t.Errorf("unexpected workspace identity: %#v", created)
	}
	if created.Repository.Provider != "gitlab" || created.Repository.RepositoryID != "platform/project-7" {
		t.Errorf("unexpected repository: %#v", created.Repository)
	}
	if created.BaseSHA != "1111111111111111111111111111111111111111" || created.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("unexpected immutable refs: %#v", created)
	}
	if created.TargetBranch != "master" || created.TargetSHA != "3333333333333333333333333333333333333333" {
		t.Errorf("expected the frozen master snapshot in Workspace, got %#v", created)
	}
	if created.State != "REGISTERED" || created.Version != 1 || created.CreatedAt == "" {
		t.Errorf("unexpected workspace state: %#v", created)
	}
}

func TestCreateWorkspaceRejectsAnUnknownRepository(t *testing.T) {
	handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrRepositoryNotFound
		},
	), "")
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
	handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrBaseCommitNotFound
		},
	), "")
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
	handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrHeadCommitNotFound
		},
	), "")
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
	handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return repository.ErrVerificationUnavailable
		},
	), "")
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
	handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			verificationAttempts++
			if verificationAttempts > 1 {
				return repository.ErrVerificationUnavailable
			}
			return nil
		},
	), "")
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
