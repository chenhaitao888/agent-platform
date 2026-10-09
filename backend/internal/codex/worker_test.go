package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
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
