package apiapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/review"
)

func cleanDeployment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AGENT_PLATFORM_AUTH_FILE", "AGENT_PLATFORM_API_ADDR", "AGENT_PLATFORM_GITLAB_BASE_URL", "AGENT_PLATFORM_GITLAB_TOKEN", "AGENT_PLATFORM_WORKSPACE_ROOT",
		"AGENT_PLATFORM_GITLAB_CA_FILE", "AGENT_PLATFORM_GIT_BINARY", "AGENT_PLATFORM_REVIEW_ENABLED", "AGENT_PLATFORM_DOCKER_BINARY",
		"AGENT_PLATFORM_DOCKER_HOST", "AGENT_PLATFORM_REVIEW_WORKER_IMAGE", "AGENT_PLATFORM_REVIEW_MODEL", "AGENT_PLATFORM_MODEL_BASE_URL",
		"AGENT_PLATFORM_MODEL_SERVICE_PRINCIPAL", "AGENT_PLATFORM_MODEL_CREDENTIAL_FILE", "AGENT_PLATFORM_MODEL_CA_FILE",
		"AGENT_PLATFORM_REVIEW_TIMEOUT", "AGENT_PLATFORM_REVIEW_SECCOMP_POLICY",
	} {
		t.Setenv(key, "")
	}
}

func TestAPIEnvironmentRefusesIncompleteReviewDeployment(t *testing.T) {
	cleanDeployment(t)
	t.Setenv("AGENT_PLATFORM_REVIEW_ENABLED", "true")
	app, err := apiapp.FromEnvironment(context.Background())
	if err == nil || app != nil || !strings.Contains(err.Error(), "AGENT_PLATFORM_REVIEW_ENABLED") {
		t.Fatalf("incomplete enabled review must stop startup: %v", err)
	}
}

func TestAPIReviewFieldsRequireExplicitEnablement(t *testing.T) {
	for _, key := range []string{"DOCKER_HOST", "MODEL_BASE_URL", "REVIEW_TIMEOUT"} {
		t.Run(key, func(t *testing.T) {
			cleanDeployment(t)
			t.Setenv("AGENT_PLATFORM_"+key, "untrusted-value")
			app, err := apiapp.FromEnvironment(context.Background())
			if app != nil || err == nil || strings.Contains(err.Error(), "untrusted-value") {
				t.Fatal("disabled partial deployment was accepted or echoed")
			}
		})
	}
}

func TestAPIWithoutDeploymentKeepsHealthAvailableAndRepositoryAdmissionClosed(t *testing.T) {
	cleanDeployment(t)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewRecorder()
	app.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatal("health unavailable")
	}
	request := `{"requestId":"create","idempotencyKey":"create","tenantId":"test","type":"PR_REVIEW","goal":"review","repository":{"provider":"gitlab","repositoryId":"42","headSha":"` + strings.Repeat("1", 40) + `"}}`
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(request)))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthenticated") {
		t.Fatalf("unconfigured API admitted repository work: %d %s", response.Code, response.Body.String())
	}
}

func TestAPIUsesDeploymentGitLabTrustWithoutEnablingModelExecution(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, false)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	createAPITask(t, app.Handler, fixture)
}

func prepareAPITask(t *testing.T, handler http.Handler, fixture deploymentFixture) {
	t.Helper()
	createAPITask(t, handler, fixture)
	apiWrite(t, handler, "PATCH", "/api/v1/tasks/task-1", `{"requestId":"queue","idempotencyKey":"queue","tenantId":"test","expectedVersion":1,"status":"QUEUED"}`, 200)
	apiWrite(t, handler, "POST", "/api/v1/tasks/task-1/workspace", `{"requestId":"register","idempotencyKey":"register","tenantId":"test"}`, 201)
	apiWrite(t, handler, "POST", "/api/v1/tasks/task-1/workspace/prepare", `{"requestId":"prepare","idempotencyKey":"prepare","tenantId":"test","expectedVersion":1}`, 200)
}

func apiWrite(t *testing.T, handler http.Handler, method, path, body string, want int) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}

func TestAPIEnabledDeploymentArchivesModelFindingsAndReplaysThroughPublicRoutes(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepareAPITask(t, app.Handler, fixture)
	result := apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`, 201)
	var operation struct {
		State    string `json:"state"`
		Artifact struct {
			ID string `json:"id"`
		} `json:"artifact"`
		Runtime review.RuntimeIdentity `json:"runtime"`
	}
	if err := json.Unmarshal(result, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.State != "SUCCEEDED" || operation.Artifact.ID == "" || operation.Runtime.ModelTransport != "stdio" || operation.Runtime.ServicePrincipal != "api-review-service" {
		t.Fatal("deployment did not publish its review result and runtime")
	}
	content := apiWrite(t, app.Handler, "GET", "/api/v1/artifacts/"+operation.Artifact.ID+"/content?tenantId=test", "", 200)
	if _, err := review.ParseFindings(content, fixture.base, fixture.head); err != nil {
		t.Fatal(err)
	}
	replayed := apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"replay","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`, 200)
	if !bytes.Equal(result, replayed) {
		t.Fatal("deployment review replay changed")
	}
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("replay called the model again")
	}
}

func TestAPIDisabledDeploymentPreparesWorkspaceAndKeepsReviewUnavailable(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, false)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepareAPITask(t, app.Handler, fixture)
	apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`, 503)
	if fixture.modelCalls.Load() != 0 {
		t.Fatal("disabled API invoked a model")
	}
}

func TestAPIStartupDoesNotReadModelCredentialButExecutionFailsClosedAndReplays(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	if err := os.Remove(fixture.credential); err != nil {
		t.Fatal(err)
	}
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal("startup read model credentials: ", err)
	}
	prepareAPITask(t, app.Handler, fixture)
	body := `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`
	first := apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", body, 502)
	second := apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", body, 502)
	if !bytes.Equal(first, second) || fixture.modelCalls.Load() != 0 {
		t.Fatal("missing credential was retried at the model boundary")
	}
	latest := apiWrite(t, app.Handler, "GET", "/api/v1/tasks/task-1/review?tenantId=test", "", 200)
	if strings.Contains(string(latest), `"artifact"`) || !strings.Contains(string(latest), `"state":"FAILED"`) {
		t.Fatal("missing credential published an artifact")
	}
}

func TestAPIRejectsUnsafeDeploymentBeforeOpeningDocker(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{"REVIEW_ENABLED", "1"}, {"REVIEW_TIMEOUT", "0"}, {"REVIEW_TIMEOUT", "3m"}, {"API_ADDR", "0.0.0.0:8080"}, {"API_ADDR", ":8080"},
		{"MODEL_BASE_URL", "http://models.example.test"}, {"REVIEW_WORKER_IMAGE", "worker:latest"}, {"REVIEW_SECCOMP_POLICY", "unconfined"}, {"MODEL_CA_FILE", "relative.pem"},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			t.Setenv("AGENT_PLATFORM_"+test.key, test.value)
			app, err := apiapp.FromEnvironment(context.Background())
			if err == nil || app != nil {
				t.Fatal("unsafe deployment accepted")
			}
			if _, err := os.Stat(filepath.Join(fixture.directory, "commands.jsonl")); !os.IsNotExist(err) {
				t.Fatal("invalid deployment reached Docker")
			}
		})
	}
}

func TestAPIRefusesUnsupportedWorkerWithoutFallbackOrInference(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	if err := os.WriteFile(filepath.Join(fixture.directory, "bad-worker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := apiapp.FromEnvironment(context.Background())
	if err == nil || app != nil || fixture.modelCalls.Load() != 0 {
		t.Fatal("unsupported Worker enabled API review")
	}
}

func TestAPIFreezesModelDeploymentAfterStartup(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PLATFORM_MODEL_BASE_URL", "https://unapproved.example.test")
	t.Setenv("AGENT_PLATFORM_REVIEW_MODEL", "unapproved-model")
	prepareAPITask(t, app.Handler, fixture)
	apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`, 201)
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("startup model deployment was replaced by later environment changes")
	}
}

func TestAPIReviewBudgetCancelsModelAndPublishesNoArtifact(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	t.Setenv("AGENT_PLATFORM_REVIEW_TIMEOUT", "1s")
	if err := os.WriteFile(filepath.Join(fixture.directory, "model-hang"), []byte("hang"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepareAPITask(t, app.Handler, fixture)
	started := time.Now()
	apiWrite(t, app.Handler, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"timeout","idempotencyKey":"timeout","tenantId":"test","expectedWorkspaceVersion":3}`, 504)
	if time.Since(started) > 3*time.Second {
		t.Fatal("review exceeded the deployment budget")
	}
	latest := apiWrite(t, app.Handler, "GET", "/api/v1/tasks/task-1/review?tenantId=test", "", 200)
	if strings.Contains(string(latest), `"artifact"`) {
		t.Fatal("timed out review published an artifact")
	}
}
