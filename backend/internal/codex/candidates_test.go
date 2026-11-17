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
	"agent-platform/backend/internal/planning"
)

func TestCandidateWorkerUsesSeparateSchemaAndReadOnlyRuntime(t *testing.T) {
	in := planning.AnalysisInput{ServiceID: "S1", SnapshotHash: strings.Repeat("a", 64), BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("c", 40), Environment: "test", Goal: "checkout limit", Patch: "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -0,0 +1,1 @@\n+settings.Integer(limitKey)\n", Resources: []planning.ResourceContext{}}
	report, _ := json.Marshal(map[string]any{"schemaVersion": "model-candidates.v1", "inputHash": planning.Hash(in), "snapshotHash": in.SnapshotHash, "serviceId": in.ServiceID, "baseSha": in.BaseSHA, "headSha": in.HeadSHA, "candidates": []any{map[string]any{"kind": "config", "key": "checkout.limit", "reason": "Wrapper resolves limitKey", "evidence": []any{map[string]any{"path": "a.go", "line": 1}}}}, "questions": []any{}})
	fixture := newRunnerFixture(t, "success", report)
	in.WorktreePath = fixture.worktree
	wire := in
	wire.Patch = ""
	request, _ := json.Marshal(map[string]any{"worktreePath": in.WorktreePath, "baseSha": in.BaseSHA, "headSha": in.HeadSHA, "patch": in.Patch, "candidates": wire})
	config := codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "candidate-fixture", Timeout: time.Second}
	var output bytes.Buffer
	if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Analysis  json.RawMessage `json:"analysis"`
		Report    json.RawMessage `json:"report"`
		ErrorCode string          `json:"errorCode"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope.ErrorCode != "" || envelope.Report != nil {
		t.Fatalf("wrong protocol: %s", output.String())
	}
	if _, err := planning.ParseModelAnalysis(envelope.Analysis, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture.capture)
	if err != nil {
		t.Fatal(err)
	}
	var capture runnerCapture
	if json.Unmarshal(data, &capture) != nil {
		t.Fatal("invalid CLI capture")
	}
	resolved, _ := filepath.EvalSymlinks(fixture.worktree)
	if capture.CWD != resolved || !bytes.Equal(capture.Schema, bytes.TrimSpace(planning.ModelSchema())) {
		var actual, expected any
		_ = json.Unmarshal(capture.Schema, &actual)
		_ = json.Unmarshal(planning.ModelSchema(), &expected)
		if planning.Hash(actual) != planning.Hash(expected) || capture.CWD != resolved {
			t.Fatal("wrong schema or workspace")
		}
	}
	for _, flag := range []string{"--sandbox", "read-only", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--ask-for-approval", "never"} {
		if !containsArgument(capture.Args, flag) {
			t.Fatal("missing boundary: " + flag)
		}
	}
	if !strings.Contains(capture.Stdin, "untrusted evidence") || strings.Contains(capture.Stdin, fixture.worktree) {
		t.Fatal("prompt boundary changed")
	}
	// A request cannot replace the schema or splice a different patch/head into the analysis.
	output.Reset()
	invalid := bytes.Replace(request, []byte(`"serviceId":"S1"`), []byte(`"schema":"arbitrary","serviceId":"S1"`), 1)
	if err := codex.ServeWorker(context.Background(), config, false, bytes.NewReader(invalid), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "review_input_invalid") {
		t.Fatal("schema override accepted")
	}
}
