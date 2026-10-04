package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	"repositoryId":"platform/project-7",
	"baseSha":"1111111111111111111111111111111111111111",
	"headSha":"2222222222222222222222222222222222222222"
}`
)

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

func (repositoryVerifierFunc) Resolve(_ context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
	selection.TargetBranch = "master"
	selection.TargetSHA = "3333333333333333333333333333333333333333"
	selection.BaseSHA = testBaseSHA
	return selection, nil
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
	return NewHandlerWithRepositoryServices(repositoryVerifierFunc(
		func(context.Context, task.RepositoryReference) error {
			return nil
		},
	), "")
}

func captureJSONLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &output
}

func registerPreparationWorkspaceForTest(t *testing.T, handler http.Handler, tenantID, key string) workspace.Workspace {
	t.Helper()
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(fmt.Sprintf(`{
		"requestId":%q,"idempotencyKey":%q,"tenantId":%q,
		"type":"PR_REVIEW","goal":"Review workspace preparation",%s
	}`, "req-create-"+key, "create-"+key, tenantID, testRepositoryJSON))))
	if created.Code != http.StatusCreated {
		t.Fatalf("create Task: %d: %s", created.Code, created.Body.String())
	}
	var currentTask task.Task
	if err := json.Unmarshal(created.Body.Bytes(), &currentTask); err != nil {
		t.Fatalf("decode Task: %v", err)
	}
	queued := httptest.NewRecorder()
	handler.ServeHTTP(queued, httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/"+currentTask.ID, strings.NewReader(fmt.Sprintf(`{
		"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"expectedVersion":1,"status":"QUEUED"
	}`, "req-queue-"+key, "queue-"+key, tenantID))))
	if queued.Code != http.StatusOK {
		t.Fatalf("queue Task: %d: %s", queued.Code, queued.Body.String())
	}
	registered := httptest.NewRecorder()
	handler.ServeHTTP(registered, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+currentTask.ID+"/workspace", strings.NewReader(fmt.Sprintf(`{
		"requestId":%q,"idempotencyKey":%q,"tenantId":%q
	}`, "req-register-"+key, "register-"+key, tenantID))))
	if registered.Code != http.StatusCreated {
		t.Fatalf("register Workspace: %d: %s", registered.Code, registered.Body.String())
	}
	var result workspace.Workspace
	if err := json.Unmarshal(registered.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Workspace: %v", err)
	}
	return result
}

func postWorkspacePreparationForTest(handler http.Handler, ctx context.Context, current workspace.Workspace, key, requestID string, version uint64) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+current.TaskID+"/workspace/prepare", strings.NewReader(fmt.Sprintf(`{
		"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"expectedVersion":%d
	}`, requestID, key, current.TenantID, version))).WithContext(ctx)
	handler.ServeHTTP(response, request)
	return response
}

func assertPreparationConflictForTest(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode preparation conflict: %v", err)
	}
	if response.Code != http.StatusConflict || body.Error != code {
		t.Fatalf("expected 409 %s, got %d: %s", code, response.Code, response.Body.String())
	}
}

func getPreparationWorkspaceForTest(t *testing.T, handler http.Handler, current workspace.Workspace) workspace.Workspace {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+current.TaskID+"/workspace?tenantId="+current.TenantID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get Workspace: %d: %s", response.Code, response.Body.String())
	}
	var result workspace.Workspace
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Workspace: %v", err)
	}
	return result
}

func preparationEventsForTest(t *testing.T, handler http.Handler, current workspace.Workspace) []task.Event {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+current.TaskID+"/events?tenantId="+current.TenantID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get Task events: %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Items []task.Event `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Task events: %v", err)
	}
	return result.Items
}
