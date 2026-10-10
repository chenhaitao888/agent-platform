package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

func TestWorkerPreflightExercisesNativeSandboxWithoutModelExecution(t *testing.T) {
	for _, mode := range []string{"success", "sandbox-fail"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRunnerFixture(t, mode, validRunnerReport(t))
			var output bytes.Buffer
			err := codex.ServeWorker(context.Background(), codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "probe-only", Timeout: time.Second}, true, strings.NewReader(""), &output)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Profile         *codex.Profile `json:"profile"`
				SandboxVerified bool           `json:"sandboxVerified"`
				ErrorCode       string         `json:"errorCode"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if mode == "success" && (result.Profile == nil || !result.SandboxVerified || result.ErrorCode != "") {
				t.Fatalf("failed native sandbox check: %s", output.Bytes())
			}
			if mode == "sandbox-fail" && (result.Profile != nil || result.SandboxVerified || result.ErrorCode != "runtime_sandbox_unavailable") {
				t.Fatalf("failed sandbox marked ready: %s", output.Bytes())
			}
			if _, err := os.Stat(fixture.capture); !os.IsNotExist(err) {
				t.Fatal("metadata probe started model review")
			}
		})
	}
}

func TestWorkerReturnsValidatedFindingsAndRejectsRequestDeploymentOverrides(t *testing.T) {
	fixture := newRunnerFixture(t, "success", validRunnerReport(t))
	config := codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "deployment-model", Timeout: time.Second}
	content, err := json.Marshal(fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(content), &output); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Report json.RawMessage `json:"report"`
	}
	if err := json.Unmarshal(output.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if report, err := review.ParseFindings(message.Report, fixture.input().BaseSHA, fixture.input().HeadSHA); err != nil || len(report.Findings) != 1 {
		t.Fatalf("lost final report: %s %v", output.Bytes(), err)
	}
	output.Reset()
	request := strings.TrimSuffix(string(content), "}") + `,"model":"request-model"}`
	if err := codex.ServeWorker(context.Background(), config, false, strings.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != `{"errorCode":"review_input_invalid"}` {
		t.Fatalf("request override accepted: %s", output.String())
	}
}

func TestGatewayWorkerUsesOnlyDeploymentIdentityAndDoesNotReturnCredentials(t *testing.T) {
	fixture := newRunnerFixture(t, "success", validRunnerReport(t))
	credential := filepath.Join(t.TempDir(), "credential.json")
	writeGatewayCredential(t, credential, "review-service", time.Now().Add(time.Hour))
	data, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"input": fixture.input(), "credential": json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	config := codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "deployment-model", Timeout: 5 * time.Second, Gateway: &codex.GatewayConfig{BaseURL: "https://models.example.test", ServicePrincipal: "review-service"}}
	var output bytes.Buffer
	if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Report json.RawMessage `json:"report"`
	}
	if err := json.Unmarshal(output.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if _, err := review.ParseFindings(message.Report, fixture.input().BaseSHA, fixture.input().HeadSHA); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), fixtureGatewayToken) || fixture.readCapture(t).Env["AGENT_PLATFORM_MODEL_TOKEN"] != fixtureGatewayToken {
		t.Fatal("Worker lost or published its service credential")
	}
	for _, extra := range []string{`"gateway":{"baseUrl":"https://other.example.test"}`, `"model":"attacker-model"`, `"credentialFile":"/tmp/attacker.json"`} {
		output.Reset()
		malicious := strings.TrimSuffix(string(input), "}") + "," + extra + "}"
		if err := codex.ServeWorker(context.Background(), config, false, strings.NewReader(malicious), &output); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(output.String()) != `{"errorCode":"review_input_invalid"}` {
			t.Fatal("Worker accepted a deployment override")
		}
	}
	output.Reset()
	writeGatewayCredential(t, credential, "another-service", time.Now().Add(time.Hour))
	data, _ = os.ReadFile(credential)
	input, _ = json.Marshal(map[string]any{"input": fixture.input(), "credential": json.RawMessage(data)})
	if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != `{"errorCode":"review_credentials_unavailable"}` {
		t.Fatal("Worker accepted a different service identity")
	}
}
