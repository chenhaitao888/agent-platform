package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

type repositoryResolverFunc func(context.Context, task.RepositoryReference) (task.RepositoryReference, error)

func (resolve repositoryResolverFunc) Resolve(ctx context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
	return resolve(ctx, selection)
}

func newHandlerWithTestResolver(resolver repository.ReferenceResolver) http.Handler {
	tasks := task.NewStore()
	verifier := repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil })
	return newHandlerWithDiffReader(tasks, workspace.NewManager(tasks, verifier), repository.UnavailableDiffReader{}, resolver)
}

func TestCreateTaskStoresServerResolvedMasterSnapshot(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{
		"requestId":"req-review-base",
		"idempotencyKey":"review-base",
		"tenantId":"tenant-a",
		"type":"PR_REVIEW",
		"goal":"Review a feature branch",
		"repository":{
			"provider":"gitlab",
			"repositoryId":"platform/project-7",
			"headSha":"2222222222222222222222222222222222222222"
		}
	}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Task: expected 201, got %d: %s", response.Code, response.Body.String())
	}
	var created struct {
		Repository struct {
			TargetBranch string `json:"targetBranch"`
			TargetSHA    string `json:"targetSha"`
			BaseSHA      string `json:"baseSha"`
			HeadSHA      string `json:"headSha"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode Task: %v", err)
	}
	if created.Repository.TargetBranch != "master" || created.Repository.TargetSHA != "3333333333333333333333333333333333333333" || created.Repository.BaseSHA != "1111111111111111111111111111111111111111" || created.Repository.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("expected a frozen master/base/head snapshot, got %#v", created.Repository)
	}
}

func TestCreateTaskReplayKeepsTheFirstMasterSnapshot(t *testing.T) {
	resolveCalls := 0
	resolver := repositoryResolverFunc(func(_ context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
		resolveCalls++
		selection.TargetBranch = "master"
		selection.TargetSHA = "3333333333333333333333333333333333333333"
		selection.BaseSHA = "1111111111111111111111111111111111111111"
		return selection, nil
	})
	handler := newHandlerWithTestResolver(resolver)
	requestBody := `{
		"requestId":"req-review-base-replay",
		"idempotencyKey":"review-base-replay",
		"tenantId":"tenant-a",
		"type":"PR_REVIEW",
		"goal":"Review a feature branch",
		"repository":{
			"provider":"gitlab",
			"repositoryId":"platform/project-7",
			"baseSha":"9999999999999999999999999999999999999999",
			"headSha":"2222222222222222222222222222222222222222"
		}
	}`
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(requestBody)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"baseSha":"1111111111111111111111111111111111111111"`) || strings.Contains(first.Body.String(), `"baseSha":"9999999999999999999999999999999999999999"`) {
		t.Fatalf("legacy client baseSha must be ignored, got %s", first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(requestBody)))
	if second.Code != http.StatusOK || resolveCalls != 1 || second.Body.String() != first.Body.String() {
		t.Fatalf("replay must reuse first snapshot: status %d, resolver calls %d, body %s", second.Code, resolveCalls, second.Body.String())
	}
}

func TestCreateTaskClassifiesReviewBaseResolutionErrorsWithoutExposingDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		cause  error
		status int
		code   string
	}{
		{"missing master", repository.ErrTargetBranchNotFound, http.StatusNotFound, "repository_reference_not_found"},
		{"no common ancestor", repository.ErrReviewBaseNotFound, http.StatusUnprocessableEntity, "review_base_not_found"},
		{"GitLab unavailable", fmt.Errorf("%w: private GitLab diagnostic", repository.ErrVerificationUnavailable), http.StatusServiceUnavailable, "repository_resolution_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newHandlerWithTestResolver(repositoryResolverFunc(func(context.Context, task.RepositoryReference) (task.RepositoryReference, error) {
				return task.RepositoryReference{}, test.cause
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{
				"requestId":"req-resolution-error",
				"idempotencyKey":"resolution-error",
				"tenantId":"tenant-a",
				"type":"PR_REVIEW",
				"goal":"Review a feature branch",
				"repository":{
					"provider":"gitlab",
					"repositoryId":"platform/project-7",
					"headSha":"2222222222222222222222222222222222222222"
				}
			}`)))
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"error":"`+test.code+`"`) || strings.Contains(response.Body.String(), "private GitLab diagnostic") {
				t.Fatalf("unexpected resolution error: status %d, body %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCreateTaskFailsClosedWithoutGitLabConfiguration(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{
		"requestId":"req-without-gitlab",
		"idempotencyKey":"without-gitlab",
		"tenantId":"tenant-a",
		"type":"PR_REVIEW",
		"goal":"Review a feature branch",
		"repository":{
			"provider":"gitlab",
			"repositoryId":"platform/project-7",
			"headSha":"2222222222222222222222222222222222222222"
		}
	}`)))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "repository_resolution_unavailable") {
		t.Fatalf("unconfigured creation must fail closed, got %d: %s", response.Code, response.Body.String())
	}
}
