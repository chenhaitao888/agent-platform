package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

func TestWorkerSafeDiagnosticsSurviveBothTransports(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, tc := range []struct{ mode, body, code, diagnostic string }{
			{"success", "private-diagnostic", "review_findings_invalid", "json_invalid"},
			{"missing", "private-diagnostic", "review_output_unavailable", "output_unavailable"},
			{"success", strings.Repeat("x", review.MaxFindingsBytes+1), "review_output_too_large", "output_too_large"},
			{"symlink", "private-diagnostic", "review_output_unavailable", "output_unavailable"},
		} {
			fixture := newRunnerFixture(t, tc.mode, []byte(tc.body))
			config := codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "fixture", Timeout: 5 * time.Second}
			input, _ := json.Marshal(fixture.input())
			var output bytes.Buffer
			if relay {
				config.Gateway = &codex.GatewayConfig{BaseURL: "https://models.example.test", ServicePrincipal: "review-service"}
				input, _ = json.Marshal(map[string]any{"input": fixture.input(), "credential": map[string]string{"servicePrincipal": "review-service", "token": "fixture-only-token"}})
				if err := codex.ServeModelRelayWorker(context.Background(), config, false, io.NopCloser(bytes.NewReader(input)), &output); err != nil {
					t.Fatal(err)
				}
				var frame struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(output.Bytes(), &frame); err != nil {
					t.Fatal(err)
				}
				output.Reset()
				output.Write(frame.Result)
			} else if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(input), &output); err != nil {
				t.Fatal(err)
			}
			var message struct {
				ErrorCode      string `json:"errorCode"`
				DiagnosticCode string `json:"diagnosticCode"`
			}
			if err := json.Unmarshal(output.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			if message.ErrorCode != tc.code || message.DiagnosticCode != tc.diagnostic || strings.Contains(output.String(), "private-diagnostic") {
				t.Fatalf("relay=%v mode=%s: unsafe/missing diagnosis: %s", relay, tc.mode, output.String())
			}
		}
	}
}

func TestDockerValidatesDiagnosticEnvelopeAndSupportsOldWorkers(t *testing.T) {
	fixture := newDockerFixture(t)
	runner, err := codex.NewDockerRunner(context.Background(), fixture.config())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("diagnostic"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, want string }{
		{`{"errorCode":"review_findings_invalid","diagnosticCode":"refs_mismatch"}`, "refs_mismatch"},
		{`{"errorCode":"review_findings_invalid"}`, "findings_invalid"},
		{`{"errorCode":"review_output_unavailable"}`, "output_unavailable"},
		{`{"errorCode":"review_findings_invalid","diagnosticCode":"private-diagnostic"}`, "worker_protocol_invalid"},
		{`{"errorCode":"review_timeout","diagnosticCode":"refs_mismatch"}`, "worker_protocol_invalid"},
		{`{"errorCode":"review_output_unavailable","diagnosticCode":"refs_mismatch"}`, "worker_protocol_invalid"},
		{`{"report":{},"diagnosticCode":"refs_mismatch"}`, "worker_protocol_invalid"},
		{`{"errorCode":"review_findings_invalid","diagnosticCode":"refs_mismatch","diagnosticCode":"json_invalid"}`, "worker_protocol_invalid"},
		{`{"errorCode":"review_findings_invalid","DiagnosticCode":"refs_mismatch"}`, "worker_protocol_invalid"},
		{`{"errorCode":"review_findings_invalid","diagnosticCode":null}`, "worker_protocol_invalid"},
	} {
		if err := os.WriteFile(filepath.Join(fixture.directory, "diagnostic.json"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := runner.Run(context.Background(), fixture.input())
		if review.OutputDiagnostic(err) != tc.want || strings.Contains(err.Error(), "private-diagnostic") {
			t.Fatalf("wanted %s, got %v", tc.want, err)
		}
		if tc.want == "refs_mismatch" && !errors.Is(err, review.ErrInvalidFindings) {
			t.Fatal("sentinel compatibility lost")
		}
	}
}
