package apiapp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/workspace"
)

// This opt-in starts the API executable with an empty store. React drives the
// successful workflow over HTTP; GitLab HTTPS and Git smart HTTP are real local
// transports. Docker/Worker and model inference remain controlled substitutes.
func TestFrontendPilotFromEmptyStoreOverHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22 and frontend dependencies installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	binary := buildAPI(t)
	for _, scenario := range []string{"success", "response-loss", "invalid-findings"} {
		t.Run(scenario, func(t *testing.T) { runFrontendPilot(t, binary, node, scenario) })
	}
}

func runFrontendPilot(t *testing.T, binary, node, scenario string) {
	t.Helper()
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	configureAPIAuthentication(t,
		apiCredential("fixture-user", "test", testAPIToken, "42"),
		apiCredential("other-user", "other", "other-api-test-token-at-least-32-characters", "42"))
	if scenario != "success" {
		findings := `[{"title":"Controlled review finding","description":"Inspect the added feature line.","severity":"MEDIUM","confidence":0.8,"path":"feature.txt","startLine":1,"endLine":1}]`
		if err := os.WriteFile(filepath.Join(fixture.directory, "pilot-findings.json"), []byte(findings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "invalid-findings" {
		if err := os.WriteFile(filepath.Join(fixture.directory, "pilot-invalid-first"), []byte("once"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	process := startAPI(t, binary)
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/PilotFlow.http.test.tsx")
	command.Dir = frontend
	// Only public fixture coordinates reach the frontend process; it receives no
	// GitLab/model credentials or deployment environment from the API process.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(),
		"AGENT_PLATFORM_PILOT_URL=" + process.address, "AGENT_PLATFORM_PILOT_HEAD=" + fixture.head, "AGENT_PLATFORM_PILOT_BASE=" + fixture.base,
		"AGENT_PLATFORM_PILOT_SCENARIO=" + scenario}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("frontend pilot HTTP flow: %v\n%s", err, output)
	}
	wantExecutions := 1
	if scenario == "invalid-findings" {
		wantExecutions = 2
	}
	if fixture.modelCalls.Load() != int32(wantExecutions) {
		t.Fatal("response loss, failed replay or rejected tenant operation caused unexpected model work")
	}
	var ready workspace.Workspace
	if err := json.Unmarshal(apiNetworkWrite(t, process.address, "GET", "/api/v1/tasks/task-1/workspace?tenantId=test", "", 200), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.State != workspace.StateReady || ready.Version != 3 || ready.BaseSHA != fixture.base || ready.HeadSHA != fixture.head ||
		gitFixture(t, ready.Path, "rev-parse", "HEAD") != fixture.head || gitFixture(t, ready.Path, "status", "--porcelain") != "" {
		t.Fatal("frontend did not prepare the fixed, clean Git worktree")
	}
	var operation review.Execution
	if err := json.Unmarshal(apiNetworkWrite(t, process.address, "GET", "/api/v1/tasks/task-1/review?tenantId=test", "", 200), &operation); err != nil || operation.State != review.ExecutionSucceeded || operation.Artifact == nil {
		t.Fatal("frontend did not publish a successful review")
	}
	inputs := strings.Split(strings.TrimSpace(string(readLocalFile(t, filepath.Join(fixture.directory, "review-inputs.jsonl")))), "\n")
	if len(inputs) != wantExecutions {
		t.Fatal("unexpected number of worker inputs")
	}
	for _, raw := range inputs {
		var input review.RunInput
		if err := json.Unmarshal([]byte(raw), &input); err != nil || input.WorktreePath != ready.Path || input.BaseSHA != fixture.base || input.HeadSHA != fixture.head || !strings.Contains(input.Patch, "+feature-only change") {
			t.Fatal("review did not consume the prepared fixed Git input")
		}
	}
	process.stop(t)
	logs := string(readLocalFile(t, process.logPath))
	for _, secret := range []string{testAPIToken, "other-api-test-token-at-least-32-characters", "api-gitlab-token", "api-model-token"} {
		if strings.Contains(logs, secret) {
			t.Fatal("API logs disclosed a fixture credential")
		}
	}
}
