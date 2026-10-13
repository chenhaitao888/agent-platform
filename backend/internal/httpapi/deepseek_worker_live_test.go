package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

// Explicit opt-in: paid inference using only a freshly generated toy repository.
// Never prints the credential file or provider diagnostics. Ordinary CI skips it.
func TestDeepSeekWorkerLiveIntegration(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_DEEPSEEK_LIVE_TEST") != "1" {
		t.Skip("live model test is opt-in")
	}
	credential, image, endpoint := os.Getenv("AGENT_PLATFORM_MODEL_CREDENTIAL_FILE"), os.Getenv("AGENT_PLATFORM_CODEX_IMAGE"), os.Getenv("AGENT_PLATFORM_DOCKER_HOST")
	if credential == "" || image == "" || endpoint == "" {
		t.Fatal("provide explicit credential path, immutable Worker image and local Docker endpoint")
	}
	model := os.Getenv("AGENT_PLATFORM_DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-flash"
	}
	if model != "deepseek-flash" && model != "deepseek-v4-pro" {
		t.Fatal("choose one of the user-authorized DeepSeek models")
	}
	fixture := newReviewExecutionFixtureWithRunner(t, "live-regression", 90*time.Second, func(root string) review.Runner {
		runner, err := codex.NewDockerRunner(context.Background(), codex.DockerConfig{Binary: "docker", Endpoint: endpoint, Image: image, WorkspaceRoot: root, Model: model, Timeout: 90 * time.Second, UID: os.Getuid(), GID: os.Getgid(), SeccompPolicy: codex.SeccompCodexBwrap, ModelRelay: true,
			Gateway: &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "pr-review-deepseek", CredentialFile: credential}})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	})
	response := fixture.post(context.Background(), "deepseek-live", "deepseek-live", fixture.ready.Version)
	if response.Code != http.StatusCreated {
		t.Fatalf("live review: HTTP %d %s", response.Code, response.Body.String())
	}
	operation := fixture.latest(t)
	if operation.State != review.ExecutionSucceeded || operation.Artifact == nil || operation.Runtime == nil || operation.Runtime.ModelTransport != "stdio" || operation.Runtime.Model != model {
		t.Fatal("live result did not archive runtime and Findings")
	}
	content := httptest.NewRecorder()
	fixture.handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+operation.Artifact.ID+"/content?tenantId="+fixture.ready.TenantID, nil))
	report, err := review.ParseFindings(content.Body.Bytes(), fixture.ready.BaseSHA, fixture.ready.HeadSHA)
	if content.Code != http.StatusOK || err != nil {
		t.Fatal("live Findings did not satisfy the platform schema")
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Path == "divider.py" && strings.Contains(strings.ToLower(finding.Description), "zero") {
			found = true
		}
	}
	if !found {
		t.Fatalf("live model missed the planted zero-division regression (%d findings)", len(report.Findings))
	}
	replay := fixture.post(context.Background(), "deepseek-live", "deepseek-replay", fixture.ready.Version)
	if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
		t.Fatal("live result replay changed")
	}
	t.Logf("model=%s state=%s findings=%d zeroDivisionDetected=true replay=200", model, operation.State, len(report.Findings))
}
