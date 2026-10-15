package revieweval_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/revieweval"
)

// Only the external inference executable is substituted. Git, diff generation,
// Codex adapter, deadlines, output validation, capture and scoring are real.
func fixtureRunner(t *testing.T, root, mode string) *codex.ExecRunner {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/codex.py")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(mode)
	binary := filepath.Join(t.TempDir(), "codex-fixture")
	if err := os.WriteFile(binary, []byte("#!"+python+"\nMODE = "+string(encoded)+"\n"+string(fixture)), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{Binary: binary, WorkspaceRoot: root, Model: "controlled-evaluation", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestCaptureFixedGitCasesAndIndependentDeadlines(t *testing.T) {
	root := t.TempDir()
	runner := fixtureRunner(t, root, "timeout-tenant")
	capture, err := revieweval.Record(context.Background(), runner, revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: time.Second, Evidence: "controlled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.Results) != 6 || capture.Results[0].State != "succeeded" || capture.Results[1].State != "timeout" || capture.Results[1].Report != nil || capture.Results[2].State != "succeeded" {
		t.Fatalf("a timeout must not erase later cases or become a clean pass: %+v", capture.Results)
	}
	for _, r := range capture.Results {
		if len(r.BaseSHA) != 40 || len(r.HeadSHA) != 40 || r.BaseSHA == r.HeadSHA || len(r.PatchSHA256) != 64 {
			t.Fatal("missing immutable input identity")
		}
	}
	if capture.Runtime.Model != "controlled-evaluation" || capture.Runtime.BinarySHA256 == "" {
		t.Fatal("missing actual runtime identity")
	}
	if _, err := revieweval.Score(capture, nil); err != nil {
		t.Fatal(err)
	}
	// Ground truth is deliberately excluded from the files sent to the model.
	entries, err := os.ReadDir(filepath.Join(root, "workspace-1", "worktree"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".git" && e.Name() != "divider.py" {
			t.Fatalf("unexpected model input: %s", e.Name())
		}
	}
	// A fresh run materializes the identical commits and patches.
	secondRoot := t.TempDir()
	second, err := revieweval.Record(context.Background(), fixtureRunner(t, secondRoot, "normal"), revieweval.Config{WorkspaceRoot: secondRoot, GitBinary: "git", Timeout: time.Second, Evidence: "controlled"})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range second.Results {
		first := capture.Results[i]
		if r.BaseSHA != first.BaseSHA || r.HeadSHA != first.HeadSHA || r.PatchSHA256 != first.PatchSHA256 {
			t.Fatal("suite identity depends on temporary location or clock")
		}
	}
}

func TestCaptureRejectsInvalidOutputAndStopsOnCancellation(t *testing.T) {
	for _, mode := range []string{"wrong-refs", "invalid-json", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			capture, err := revieweval.Record(context.Background(), fixtureRunner(t, root, mode), revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: time.Second, Evidence: "controlled"})
			if err != nil {
				t.Fatal(err)
			}
			want := "invalid_output"
			if mode == "nonzero" {
				want = "execution_failed"
			}
			for _, r := range capture.Results {
				if r.State != want || r.Report != nil {
					t.Fatalf("%s produced %+v", mode, r)
				}
			}
			data, _ := json.Marshal(capture)
			if strings.Contains(string(data), "private-diagnostic") {
				t.Fatal("raw process output escaped")
			}
			report, err := revieweval.Score(capture, nil)
			if err != nil || report.Summary.FalseNegatives != 4 || report.Summary.Recall == nil || report.Summary.Recall.Value != 0 {
				t.Fatal("failed executions must remain misses", err)
			}
		})
	}
	root := t.TempDir()
	runner := fixtureRunner(t, root, "normal")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	capture, err := revieweval.Record(ctx, runner, revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: time.Second, Evidence: "controlled"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range capture.Results {
		if r.State != "skipped" {
			t.Fatal("canceled suite launched a case")
		}
	}
}

func TestGitPreparationTimeoutRemainsARecordedTimeout(t *testing.T) {
	root := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(t.TempDir(), "slow-git")
	if err := os.WriteFile(git, []byte("#!"+python+"\nimport time\ntime.sleep(30)\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	capture, err := revieweval.Record(context.Background(), fixtureRunner(t, root, "normal"), revieweval.Config{WorkspaceRoot: root, GitBinary: git, Timeout: 50 * time.Millisecond, Evidence: "controlled"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range capture.Results {
		if r.State != "timeout" || r.Report != nil {
			t.Fatal("preparation deadline was hidden", r)
		}
	}
	report, err := revieweval.Score(capture, nil)
	if err != nil || report.Summary.States["timeout"] != 6 || report.Summary.FalseNegatives != 4 {
		t.Fatal("incomplete preparation lost failure evidence", err)
	}
}
