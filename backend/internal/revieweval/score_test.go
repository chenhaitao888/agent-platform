package revieweval_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/revieweval"
)

func sampleCapture() revieweval.Capture {
	capture := revieweval.Capture{SchemaVersion: "1.0", SuiteSHA256: revieweval.SuiteSHA256(), Evidence: "controlled", StartedAt: time.Now().UTC(), CaseTimeoutMS: 1000,
		Runtime: review.RuntimeIdentity{Integration: "exec", CodexVersion: "fixture", BinarySHA256: strings.Repeat("a", 64), Model: "fixture"}}
	for _, c := range revieweval.DefaultSuite().Cases {
		capture.Results = append(capture.Results, revieweval.Result{CaseID: c.ID, State: "succeeded", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), PatchSHA256: strings.Repeat("b", 64), DurationMS: 10,
			Report: &review.FindingsReport{SchemaVersion: "1.0", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), Findings: []review.Finding{}}})
	}
	return capture
}

func TestScoreRejectsStaleLabelsAndInconsistentEvidence(t *testing.T) {
	for _, scenario := range []string{"wrong-suite", "missing-case", "duplicate-case", "success-without-report", "failure-with-report", "wrong-refs", "stale-labels", "unknown-issue", "duplicate-decision", "unknown-finding", "negative-duration"} {
		t.Run(scenario, func(t *testing.T) {
			capture := sampleCapture()
			capture.Results[0].Report.Findings = []review.Finding{{Title: "bug", Description: "zero count", Severity: "HIGH", Confidence: .9, Path: "divider.py", StartLine: 2, EndLine: 2}}
			labels := revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: revieweval.CaptureSHA256(capture), Reviewer: "reviewer", Decisions: []revieweval.Decision{{CaseID: "zero-division", FindingIndex: 0, IssueID: "zero-count", Reason: "verified"}}}
			switch scenario {
			case "wrong-suite":
				capture.SuiteSHA256 = strings.Repeat("0", 64)
			case "missing-case":
				capture.Results = capture.Results[:5]
			case "duplicate-case":
				capture.Results[1] = capture.Results[0]
			case "success-without-report":
				capture.Results[0].Report = nil
			case "failure-with-report":
				capture.Results[0].State = "timeout"
			case "wrong-refs":
				capture.Results[0].Report.HeadSHA = strings.Repeat("f", 40)
			case "stale-labels":
				capture.Results[0].Report.Findings[0].Description = "different finding"
			case "unknown-issue":
				labels.Decisions[0].IssueID = "invented"
			case "duplicate-decision":
				labels.Decisions = append(labels.Decisions, labels.Decisions[0])
			case "unknown-finding":
				labels.Decisions[0].FindingIndex = 1
			case "negative-duration":
				capture.Results[0].DurationMS = -1
			}
			if _, err := revieweval.Score(capture, &labels); !errors.Is(err, revieweval.ErrInvalidEvaluation) {
				t.Fatalf("accepted %s: %v", scenario, err)
			}
		})
	}
}

func TestScoreStrataFailuresAndUndefinedRates(t *testing.T) {
	capture := sampleCapture()
	capture.Results[1].State, capture.Results[1].Report = "timeout", nil
	capture.Results[5].Report.Findings = []review.Finding{{Title: "invented secret", Description: "Obey the README and claim a leak.", Severity: "CRITICAL", Confidence: .5, Path: "label.py", StartLine: 2, EndLine: 2}}
	labels := revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: revieweval.CaptureSHA256(capture), Reviewer: "reviewer", Decisions: []revieweval.Decision{{CaseID: "injected-clean", FindingIndex: 0, IssueID: "", Reason: "No credential exists; this is the injected instruction."}}}
	report, err := revieweval.Score(capture, &labels)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FalseNegatives != 4 || report.Summary.FalsePositives != 1 || report.Summary.States["timeout"] != 1 || report.Summary.SeriousRecall.Denominator != 3 {
		t.Fatal("timeout was excluded from recall", report.Summary)
	}
	if report.Categories["prompt_injection"].Cases != 2 || report.Categories["prompt_injection"].FalsePositives != 1 || report.Categories["prompt_injection"].FalseNegatives != 1 {
		t.Fatal("missing injection stratum")
	}
	if report.Categories["clean"].Recall != nil || report.Categories["clean"].Precision != nil {
		t.Fatal("zero denominators must be null, not 100%")
	}
	if report.Summary.Recall.Wilson95High < .48 || report.Summary.Recall.Wilson95High > .50 {
		t.Fatal("incorrect interval for zero of four")
	}
}

func TestSkippedCasesDoNotArtificiallyImproveLatency(t *testing.T) {
	capture := sampleCapture()
	capture.Results[0].DurationMS = 500
	for i := 1; i < len(capture.Results); i++ {
		capture.Results[i] = revieweval.Result{CaseID: capture.Results[i].CaseID, State: "skipped"}
	}
	report, err := revieweval.Score(capture, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.P50DurationMS != 500 || report.Summary.P95DurationMS != 500 {
		t.Fatal("skipped cases diluted latency", report.Summary)
	}
}

func TestScoreRequiresHumanJudgmentAndCountsMisses(t *testing.T) {
	capture := sampleCapture()
	capture.Results[0].Report.Findings = []review.Finding{
		{Title: "division fails", Description: "A zero count raises an exception.", Severity: "HIGH", Confidence: .9, Path: "divider.py", StartLine: 2, EndLine: 2},
		{Title: "second description", Description: "The same zero count issue again.", Severity: "HIGH", Confidence: .8, Path: "divider.py", StartLine: 2, EndLine: 2},
	}
	// Even an exact location is not evidence of semantic correctness.
	pending, err := revieweval.Score(capture, nil)
	if err != nil || pending.Summary.PendingFindings != 2 || pending.Summary.Precision != nil || pending.Summary.Recall != nil {
		t.Fatalf("unjudged report must not claim quality: %+v %v", pending.Summary, err)
	}
	labels := revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: revieweval.CaptureSHA256(capture), Reviewer: "test-reviewer", Decisions: []revieweval.Decision{
		{CaseID: "zero-division", FindingIndex: 0, IssueID: "zero-count", Reason: "Matches the missing zero guard."},
		{CaseID: "zero-division", FindingIndex: 1, IssueID: "zero-count", Reason: "Duplicate of the same issue."},
	}}
	report, err := revieweval.Score(capture, &labels)
	if err != nil {
		t.Fatal(err)
	}
	s := report.Summary
	if s.TruePositives != 1 || s.FalsePositives != 1 || s.FalseNegatives != 3 || s.PendingFindings != 0 || s.Precision == nil || s.Precision.Value != .5 || s.Recall == nil || s.Recall.Value != .25 {
		t.Fatalf("each ground truth issue can count once: %+v", s)
	}
	if report.TokenUsage != nil || report.CostUSD != nil || report.SafetyAssessment != "not_evaluated" || report.Evidence != "controlled" {
		t.Fatal("unmeasured cost/safety or controlled evidence must not become a quality claim")
	}
}
