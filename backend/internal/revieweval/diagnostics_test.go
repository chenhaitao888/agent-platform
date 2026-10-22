package revieweval_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"agent-platform/backend/internal/revieweval"
)

func TestScoreRejectsUnknownOrInconsistentDiagnostics(t *testing.T) {
	for _, code := range []string{"refs_mismatch", "private-diagnostic"} {
		capture := sampleCapture()
		capture.Results[0].DiagnosticCode = code
		if _, err := revieweval.Score(capture, nil); !errors.Is(err, revieweval.ErrInvalidEvaluation) {
			t.Fatal("success must not carry an output rejection")
		}
		capture.Results[0].State, capture.Results[0].Report = "invalid_output", nil
		report, err := revieweval.Score(capture, nil)
		if code == "refs_mismatch" {
			if err != nil || report.Cases[0].DiagnosticCode != code {
				t.Fatal("lost safe diagnostic", err)
			}
		} else if !errors.Is(err, revieweval.ErrInvalidEvaluation) {
			t.Fatal("unknown diagnostic accepted")
		}
	}
}

func TestCaptureRetainsSafeOutputDiagnostics(t *testing.T) {
	for mode, want := range map[string]string{"wrong-refs": "refs_mismatch", "invalid-json": "json_invalid", "nonzero": ""} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			capture, err := revieweval.Record(context.Background(), fixtureRunner(t, root, mode), revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: 2 * time.Second, Evidence: "controlled"})
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(capture)
			var wire struct {
				Results []struct {
					DiagnosticCode string `json:"diagnosticCode"`
				} `json:"results"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			for _, result := range wire.Results {
				if result.DiagnosticCode != want {
					t.Fatalf("want diagnostic %q, got %q", want, result.DiagnosticCode)
				}
			}
			if _, err := revieweval.Score(capture, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOriginalCaptureHashAndPendingJudgmentRemainCompatible(t *testing.T) {
	data, err := os.ReadFile("../../../docs/evaluations/2026-10-15-flash/capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture revieweval.Capture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if got := revieweval.CaptureSHA256(capture); got != "587208f6a3f1172d3fb172f62ef8dbbe24b8d7d805677aea9cfca371e091e767" {
		t.Fatalf("historical capture identity changed: %s", got)
	}
	data, err = os.ReadFile("../../../docs/evaluations/2026-10-15-flash/adjudications.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	var labels revieweval.Adjudications
	if err := json.Unmarshal(data, &labels); err != nil {
		t.Fatal(err)
	}
	report, err := revieweval.Score(capture, &labels)
	if err != nil || report.Summary.PendingFindings != 1 || report.Summary.Precision != nil || report.Summary.States["invalid_output"] != 1 {
		t.Fatalf("historical evidence changed: %+v %v", report.Summary, err)
	}
}
