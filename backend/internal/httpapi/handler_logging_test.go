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
)

func TestPrepareWorkspaceFailureLogsCauseAndCorrelation(t *testing.T) {
	logOutput := captureJSONLogs(t)
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return fmt.Errorf("clone failed: %w", errors.New("remote rejected commit"))
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-log",
		"idempotencyKey":"prepare-log",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "remote rejected commit") {
		t.Fatalf("expected stable 503 without internal cause, got %d: %s", response.Code, response.Body.String())
	}
	var record struct {
		Level     string `json:"level"`
		RequestID string `json:"requestId"`
		TenantID  string `json:"tenantId"`
		TaskID    string `json:"taskId"`
		Cause     string `json:"cause"`
	}
	if err := json.NewDecoder(logOutput).Decode(&record); err != nil {
		t.Fatalf("decode failure log: %v", err)
	}
	if record.Level != "ERROR" || record.RequestID != "req-prepare-log" || record.TenantID != "tenant-a" || record.TaskID != "task-1" || !strings.Contains(record.Cause, "remote rejected commit") {
		t.Fatalf("expected correlated root cause in log, got %#v", record)
	}
}

func TestPrepareWorkspaceFailureRedactsGitLabTokenFromLog(t *testing.T) {
	const token = "service-secret-token"
	// 进程环境故意放另一份值：遮盖必须跟随传给 handler 的真实 token。
	t.Setenv("AGENT_PLATFORM_GITLAB_TOKEN", "stale-environment-token")
	logOutput := captureJSONLogs(t)
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return errors.New("remote echoed " + token)
		}),
		repository.UnavailableDiffReader{},
		t.TempDir(),
		token,
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-redaction",
		"idempotencyKey":"prepare-redaction",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected stable 503, got %d", response.Code)
	}
	if strings.Contains(response.Body.String(), token) || strings.Contains(logOutput.String(), token) {
		t.Fatal("GitLab token must not appear in the response or server log")
	}
	if !strings.Contains(logOutput.String(), "remote echoed [REDACTED]") {
		t.Fatalf("expected redacted root cause in log, got %s", logOutput.String())
	}
}

func TestWorkspaceRegistrationLogsServiceAndUnexpectedFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		failure   error
		status    int
		errorCode string
		rootCause string
	}{
		{"GitLab unavailable", fmt.Errorf("%w: GitLab returned 502", repository.ErrVerificationUnavailable), http.StatusServiceUnavailable, "repository_verification_unavailable", "GitLab returned 502"},
		{"unexpected failure", errors.New("unexpected verifier failure"), http.StatusInternalServerError, "internal_error", "unexpected verifier failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logOutput := captureJSONLogs(t)
			handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
				func(context.Context, task.RepositoryReference) error { return test.failure },
			), "")
			createQueuedTaskForWorkspaceTest(t, handler)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace", strings.NewReader(`{
				"requestId":"req-register-log",
				"idempotencyKey":"register-log",
				"tenantId":"tenant-a"
			}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || strings.Contains(response.Body.String(), test.rootCause) {
				t.Fatalf("expected stable %d without internal cause, got %d: %s", test.status, response.Code, response.Body.String())
			}
			var record struct {
				RequestID string `json:"requestId"`
				TenantID  string `json:"tenantId"`
				TaskID    string `json:"taskId"`
				ErrorCode string `json:"errorCode"`
				Cause     string `json:"cause"`
			}
			if err := json.NewDecoder(logOutput).Decode(&record); err != nil {
				t.Fatalf("decode failure log: %v", err)
			}
			if record.RequestID != "req-register-log" || record.TenantID != "tenant-a" || record.TaskID != "task-1" || record.ErrorCode != test.errorCode || !strings.Contains(record.Cause, test.rootCause) {
				t.Fatalf("unexpected correlated failure log: %#v", record)
			}
		})
	}
}

func TestGetWorkspaceDiffFailureLogsHeaderRequestID(t *testing.T) {
	logOutput := captureJSONLogs(t)
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
			return nil, fmt.Errorf("%w: git diff exited 128", repository.ErrDiffUnavailable)
		}),
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	prepareWorkspaceForTest(t, handler, "prepare-for-diff-log")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-a", nil)
	request.Header.Set("X-Request-ID", "req-read-diff-log")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "git diff exited 128") {
		t.Fatalf("expected stable 503 without Git output, got %d: %s", response.Code, response.Body.String())
	}
	var record struct {
		RequestID string `json:"requestId"`
		TenantID  string `json:"tenantId"`
		TaskID    string `json:"taskId"`
		Cause     string `json:"cause"`
	}
	if err := json.NewDecoder(logOutput).Decode(&record); err != nil {
		t.Fatalf("decode failure log: %v", err)
	}
	if record.RequestID != "req-read-diff-log" || record.TenantID != "tenant-a" || record.TaskID != "task-1" || !strings.Contains(record.Cause, "git diff exited 128") {
		t.Fatalf("unexpected GET diff failure log: %#v", record)
	}
}
