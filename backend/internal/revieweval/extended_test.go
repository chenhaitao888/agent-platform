package revieweval_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/revieweval"
)

func TestExtendedGroundTruthHasObservableRegressionsAndACoordinatedSafeChange(t *testing.T) {
	suite, _, err := revieweval.LoadSuite("extended-v1")
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	observations := map[string][2]string{
		"cross-file-rate-unit":       {"120.0", "2100"},
		"cross-file-return-contract": {`{"id": 42, "total": 100}`, `"KeyError"`},
		"tenant-write-guard":         {`{"allowed": false, "title": "original"}`, `{"allowed": true, "title": "changed"}`},
		"tenant-cache-key":           {`"tenant-b"`, `"tenant-a"`},
		"safe-rate-migration":        {"[0.0, 12.0, 120.0, -12.0]", "[0.0, 12.0, 120.0, -12.0]"},
		"policy-timeout-fail-open":   {`{"allowed": false, "present": true}`, `{"allowed": true, "present": false}`},
	}
	for _, c := range suite.Cases[6:] {
		t.Run(c.ID, func(t *testing.T) {
			for i, files := range []map[string]string{c.Base, c.Head} {
				data, _ := json.Marshal(map[string]any{"caseId": c.ID, "files": files})
				command := exec.Command(python, "-I", "testdata/extended_ground_truth.py")
				command.Stdin = strings.NewReader(string(data))
				output, err := command.CombinedOutput()
				if err != nil || strings.TrimSpace(string(output)) != observations[c.ID][i] {
					t.Fatalf("snapshot %d: %s %v", i, output, err)
				}
			}
			for _, issue := range c.Issues {
				if issue.StartLine < 1 || issue.EndLine < issue.StartLine || issue.EndLine > len(strings.Split(strings.TrimSuffix(c.Head[issue.Path], "\n"), "\n")) {
					t.Fatal("invalid ground-truth coordinates")
				}
			}
		})
	}
}

func TestExtendedCaptureUsesNestedCompleteSnapshotsAndPreservesStarterPins(t *testing.T) {
	suite, sum, err := revieweval.LoadSuite("extended-v1")
	if err != nil || len(suite.Cases) != 12 || sum == revieweval.SuiteSHA256() {
		t.Fatal("missing independent extended suite", err)
	}
	if !reflect.DeepEqual(suite.Cases[:6], revieweval.DefaultSuite().Cases) {
		t.Fatal("extended suite changed starter cases")
	}
	root := t.TempDir()
	capture, err := revieweval.Record(context.Background(), fixtureRunner(t, root, "normal"), revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: 2 * time.Second, Evidence: "controlled", Suite: "extended-v1"})
	if err != nil || capture.SuiteSHA256 != sum || len(capture.Results) != 12 {
		t.Fatal("missing selected suite identity", err)
	}
	for i, result := range capture.Results {
		if result.State != "succeeded" {
			t.Fatalf("%s: %s", result.CaseID, result.State)
		}
		if i >= 6 && len(result.Report.Findings) != 0 {
			t.Fatal("fixture returned unexpected evidence")
		}
		files := map[string]string{}
		directory := filepath.Join(root, "workspace-"+strconv.Itoa(i+1), "worktree")
		err := filepath.WalkDir(directory, func(name string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			content, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(directory, name)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = string(content)
			return nil
		})
		if err != nil || !reflect.DeepEqual(files, suite.Cases[i].Head) {
			t.Fatalf("worktree does not match head (or contains labels): %s %v", result.CaseID, err)
		}
	}
	data, err := os.ReadFile("../../../docs/evaluations/2026-10-22-diagnostics/capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var historical revieweval.Capture
	if err := json.Unmarshal(data, &historical); err != nil {
		t.Fatal(err)
	}
	for i, old := range historical.Results {
		current := capture.Results[i]
		if old.BaseSHA != current.BaseSHA || old.HeadSHA != current.HeadSHA || old.PatchSHA256 != current.PatchSHA256 {
			t.Fatal("starter input pins changed")
		}
	}
	// The module move must delete the old file, not overlay new files onto base.
	worktree := filepath.Join(root, "workspace-8", "worktree")
	if _, err := os.Stat(filepath.Join(worktree, "orders", "store.py")); !os.IsNotExist(err) {
		t.Fatal("deleted base file survived in head")
	}
	if _, err := os.Stat(filepath.Join(worktree, "orders", "repository.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := revieweval.Score(capture, nil); err != nil {
		t.Fatal("extended capture cannot be scored", err)
	}
}

func TestExtendedScoreResolvesSuiteByDigestAndRejectsMismatchedEvidence(t *testing.T) {
	suite, sum, err := revieweval.LoadSuite("extended-v1")
	if err != nil {
		t.Fatal(err)
	}
	capture := sampleCapture()
	capture.SuiteSHA256 = sum
	for _, c := range suite.Cases[6:] {
		capture.Results = append(capture.Results, revieweval.Result{CaseID: c.ID, State: "succeeded", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), PatchSHA256: strings.Repeat("b", 64), DurationMS: 10,
			Report: &review.FindingsReport{SchemaVersion: "1.0", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), Findings: []review.Finding{}}})
	}
	labels := revieweval.Adjudications{SchemaVersion: "1.0", Reviewer: "controlled-ground-truth", ReviewerKind: "assistant", Rubric: "regression-with-advisories-v1"}
	for i, c := range suite.Cases {
		for _, issue := range c.Issues {
			index := len(capture.Results[i].Report.Findings)
			capture.Results[i].Report.Findings = append(capture.Results[i].Report.Findings, review.Finding{Title: issue.ID, Description: issue.Rationale, Severity: issue.Severity, Confidence: 1, Path: issue.Path, StartLine: issue.StartLine, EndLine: issue.EndLine})
			labels.Decisions = append(labels.Decisions, revieweval.Decision{CaseID: c.ID, FindingIndex: index, IssueID: issue.ID, Reason: "Controlled scoring fixture, not model quality evidence."})
		}
	}
	labels.CaptureSHA256 = revieweval.CaptureSHA256(capture)
	report, err := revieweval.Score(capture, &labels)
	if err != nil || report.Summary.TruePositives != 9 || report.Summary.SeriousIssues != 8 || report.Summary.Cases != 12 || report.Categories["cross_file"].TruePositives != 2 || report.Categories["dependency_failure"].TruePositives != 1 {
		t.Fatal("wrong selected-suite denominators", err)
	}
	for _, scenario := range []string{"starter-digest", "truncated", "wrong-issue", "unknown-digest", "stale-labels"} {
		t.Run(scenario, func(t *testing.T) {
			bad := capture
			badLabels := labels
			badLabels.Decisions = append([]revieweval.Decision{}, labels.Decisions...)
			switch scenario {
			case "starter-digest":
				bad.SuiteSHA256 = revieweval.SuiteSHA256()
			case "truncated":
				bad.Results = bad.Results[:6]
			case "wrong-issue":
				badLabels.Decisions[len(badLabels.Decisions)-1].IssueID = "zero-count"
			case "unknown-digest":
				bad.SuiteSHA256 = strings.Repeat("f", 64)
			case "stale-labels":
				badLabels.CaptureSHA256 = revieweval.CaptureSHA256(sampleCapture())
			}
			if scenario != "stale-labels" {
				badLabels.CaptureSHA256 = revieweval.CaptureSHA256(bad)
			}
			if _, err := revieweval.Score(bad, &badLabels); !errors.Is(err, revieweval.ErrInvalidEvaluation) {
				t.Fatal("mismatched evidence accepted", err)
			}
		})
	}
	// Loading a suite gives a private copy; callers cannot rewrite future inputs.
	suite.Cases[0].Head["divider.py"] = "altered"
	fresh, _, err := revieweval.LoadSuite("extended-v1")
	if err != nil || fresh.Cases[0].Head["divider.py"] == "altered" {
		t.Fatal("mutable suite leaked")
	}
}

func TestExtendedFailuresRemainInRecallAndDoNotStopLaterCases(t *testing.T) {
	root := t.TempDir()
	capture, err := revieweval.Record(context.Background(), fixtureRunner(t, root, "extended-mixed"), revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: 2 * time.Second, Evidence: "controlled", Suite: "extended-v1"})
	if err != nil {
		t.Fatal(err)
	}
	wanted := []struct{ state, diagnostic string }{
		{"invalid_output", "refs_mismatch"}, {"execution_failed", ""}, {"timeout", ""},
		{"invalid_output", "json_invalid"}, {"succeeded", ""}, {"invalid_output", "output_unavailable"},
	}
	for i, want := range wanted {
		result := capture.Results[i+6]
		if result.State != want.state || result.DiagnosticCode != want.diagnostic || (result.State != "succeeded" && result.Report != nil) {
			t.Fatalf("%s: want %+v, got %+v", result.CaseID, want, result)
		}
	}
	report, err := revieweval.Score(capture, nil)
	if err != nil || report.Summary.States["succeeded"] != 7 || report.Summary.FalseNegatives != 9 || report.Summary.Recall == nil || report.Summary.Recall.Denominator != 9 || report.Summary.SeriousRecall.Denominator != 8 {
		t.Fatal("failures changed recall denominator", err)
	}
	encoded, _ := json.Marshal(capture)
	if strings.Contains(string(encoded), "private-diagnostic") {
		t.Fatal("raw failure text leaked")
	}
	// Cancellation skips every selected case, preserving the extended denominator.
	root = t.TempDir()
	runner := fixtureRunner(t, root, "normal")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := revieweval.Record(ctx, runner, revieweval.Config{WorkspaceRoot: root, GitBinary: "git", Timeout: time.Second, Evidence: "controlled", Suite: "extended-v1"})
	if err != nil {
		t.Fatal(err)
	}
	report, err = revieweval.Score(canceled, nil)
	if err != nil || report.Summary.States["skipped"] != 12 || report.Summary.FalseNegatives != 9 || report.Summary.DurationSamples != 0 {
		t.Fatal("canceled suite lost cases", err)
	}
}

func TestVersionedSuitesPreserveHistoricalScores(t *testing.T) {
	root := "../../../docs/evaluations"
	for _, fixture := range []struct {
		directory string
		exact     int
		under     int
		over      int
	}{
		{"2026-10-15-flash", 1, 2, 1},
		{"2026-10-19-rubric", 1, 2, 1},
		{"2026-10-22-diagnostics", 3, 0, 1},
		{"2026-10-22-extended", 5, 3, 1},
	} {
		t.Run(fixture.directory, func(t *testing.T) {
			directory := fixture.directory
			load := func(name string, target any) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, target); err != nil {
					t.Fatal(err)
				}
			}
			source := directory
			if directory == "2026-10-19-rubric" {
				source = "2026-10-15-flash"
			}
			var capture revieweval.Capture
			var labels revieweval.Adjudications
			var pricing revieweval.Pricing
			var expected revieweval.Report
			load(source+"/capture.json", &capture)
			load(source+"/pricing.json", &pricing)
			load(directory+"/adjudications.draft.json", &labels)
			load(directory+"/report.draft.json", &expected)
			actual, err := revieweval.ScoreWithPricing(capture, &labels, &pricing)
			if err != nil {
				t.Fatal(err)
			}
			s := actual.SeverityCalibration
			if s == nil || s.ExactMatches != fixture.exact || s.UnderstatedIssues != fixture.under || s.OverstatedIssues != fixture.over || s.MatchedIssues != expected.Summary.TruePositives || s.ExpectedIssues != expected.Summary.ExpectedIssues || s.MissedIssues != expected.Summary.FalseNegatives || s.PendingFindings != expected.Summary.PendingFindings || s.ExcludedFindings != expected.Summary.FalsePositives+expected.Summary.Advisories {
				t.Fatalf("incorrect calibration of historical evidence: %+v", s)
			}
			if expected.Summary.PendingFindings > 0 {
				if s.ExactMatchRate != nil {
					t.Fatal("pending historical judgments acquired a quality rate")
				}
			} else if s.ExactMatchRate == nil || s.ExactMatchRate.Numerator != fixture.exact || s.ExactMatchRate.Denominator != expected.Summary.TruePositives {
				t.Fatal("wrong exact-match denominator")
			}
			// Calibration is an additive report field, absent from the immutable
			// historical reports. Compare every pre-existing field unchanged.
			if expected.SeverityCalibration != nil {
				t.Fatal("historical report was rewritten")
			}
			actual.SeverityCalibration = nil
			// Scorer identity belongs to the command, not the scoring function.
			expected.ScorerSHA256 = ""
			// Pre-step40 reports omitted the now-explicit legacy rubric name.
			if expected.Rubric == "" {
				expected.Rubric = "closed-issues-v1"
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("historical scoring semantics or canonical hashes changed")
			}
		})
	}
}
