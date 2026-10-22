package revieweval_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/revieweval"
)

func TestSeverityCalibrationSeparatesIssueDetectionFromSeverity(t *testing.T) {
	capture := sampleCapture()
	labels := revieweval.Adjudications{SchemaVersion: "1.0", Reviewer: "controlled-test", ReviewerKind: "assistant"}
	// One match, one underestimate, one overestimate and one missing issue.
	for _, example := range []struct {
		caseIndex int
		severity  string
	}{{0, "HIGH"}, {1, "LOW"}, {2, "HIGH"}} {
		c := revieweval.DefaultSuite().Cases[example.caseIndex]
		issue := c.Issues[0]
		capture.Results[example.caseIndex].Report.Findings = []review.Finding{{Title: "regression", Description: issue.Rationale, Severity: example.severity, Confidence: 1, Path: issue.Path, StartLine: issue.StartLine, EndLine: issue.EndLine}}
		labels.Decisions = append(labels.Decisions, revieweval.Decision{CaseID: c.ID, FindingIndex: 0, IssueID: issue.ID, Reason: "Controlled semantic match."})
	}
	labels.CaptureSHA256 = revieweval.CaptureSHA256(capture)
	report, err := revieweval.Score(capture, &labels)
	if err != nil {
		t.Fatal(err)
	}
	s := report.SeverityCalibration
	if s == nil || s.ExpectedIssues != 4 || s.MatchedIssues != 3 || s.ExactMatches != 1 || s.UnderstatedIssues != 1 || s.OverstatedIssues != 1 || s.MissedIssues != 1 || s.PendingIssues != 0 {
		t.Fatalf("severity must remain distinct from detection: %+v", s)
	}
	if s.ExactMatchRate == nil || s.ExactMatchRate.Numerator != 1 || s.ExactMatchRate.Denominator != 3 || s.ConfusionMatrix["CRITICAL"]["LOW"] != 1 || s.ConfusionMatrix["MEDIUM"]["HIGH"] != 1 || s.ConfusionMatrix["HIGH"]["HIGH"] != 1 {
		t.Fatalf("wrong calibration denominator or matrix: %+v", s)
	}
	if report.Summary.TruePositives != 3 || report.Summary.FalseNegatives != 1 || report.Summary.SeriousFound != 2 || report.Summary.SeriousRecall.Denominator != 3 || report.AdjudicationKind != "assistant" {
		t.Fatal("calibration changed detection metrics or provenance")
	}
	if len(s.Issues) != 4 || s.Issues[0].FindingIndex == nil || *s.Issues[0].FindingIndex != 0 || s.Issues[3].State != "missed" || s.Issues[3].FindingIndex != nil || s.Issues[3].ReportedSeverity != "" {
		t.Fatalf("missing per-issue evidence: %+v", s.Issues)
	}
}

func TestSeverityCalibrationUsesFirstMatchAndExcludesDuplicatesFPAndAdvisories(t *testing.T) {
	capture, labels := advisoryCapture()
	capture.Results[4].Report.Findings[0].Severity = "LOW"
	duplicate := capture.Results[4].Report.Findings[0]
	duplicate.Title, duplicate.Severity = "Duplicate with preferred severity", "HIGH"
	capture.Results[4].Report.Findings = append(capture.Results[4].Report.Findings, duplicate)
	capture.Results[3].Report.Findings = []review.Finding{{Title: "Invented defect", Description: "Controlled false positive", Severity: "CRITICAL", Confidence: 1, Path: "label.py", StartLine: 2, EndLine: 2}}
	labels.Decisions = append(labels.Decisions,
		revieweval.Decision{CaseID: "injected-zero-division", FindingIndex: 2, IssueID: "zero-count", Reason: "Duplicate of index zero."},
		revieweval.Decision{CaseID: "safe-refactor", FindingIndex: 0, Reason: "No regression."})
	labels.CaptureSHA256 = revieweval.CaptureSHA256(capture)
	before, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	report, err := revieweval.Score(capture, &labels)
	if err != nil {
		t.Fatal(err)
	}
	s := report.SeverityCalibration
	if s.Policy != "first-adjudicated-match-v1" || s.MatchedIssues != 1 || s.UnderstatedIssues != 1 || s.ExactMatches != 0 || s.ExcludedFindings != 3 || s.ExactMatchRate.Value != 0 || s.ConfusionMatrix["HIGH"]["LOW"] != 1 || s.ConfusionMatrix["HIGH"]["HIGH"] != 0 {
		t.Fatalf("duplicates or non-regression findings inflated severity accuracy: %+v", s)
	}
	if report.Summary.FalsePositives != 2 || report.Summary.Advisories != 1 || report.Summary.Precision.Value != .25 || report.SafetyAssessment != "not_evaluated" {
		t.Fatal("non-regression findings left detection denominator or implied safety")
	}
	// Decision order must not select a more favorable duplicate.
	slices.Reverse(labels.Decisions)
	reordered, err := revieweval.Score(capture, &labels)
	if err != nil || !reflect.DeepEqual(s, reordered.SeverityCalibration) {
		t.Fatal("decision order changed severity", err)
	}
	after, err := json.Marshal(capture)
	if err != nil || string(before) != string(after) {
		t.Fatal("scoring mutated capture", err)
	}
}

func TestSeverityCalibrationKeepsUnjudgedFindingsPending(t *testing.T) {
	for _, scenario := range []string{"unmatched-issue", "earlier-duplicate", "clean-case"} {
		t.Run(scenario, func(t *testing.T) {
			capture := sampleCapture()
			finding := review.Finding{Title: "Regression", Description: "Deleted guard", Severity: "HIGH", Confidence: 1, Path: "divider.py", StartLine: 2, EndLine: 2}
			capture.Results[0].Report.Findings = []review.Finding{finding}
			labels := revieweval.Adjudications{SchemaVersion: "1.0", Reviewer: "controlled-test"}
			wantMatched, wantMissed, wantPendingIssues := 0, 3, 1
			switch scenario {
			case "earlier-duplicate":
				capture.Results[0].Report.Findings[0].Severity = "LOW"
				finding.Title = "Later matched finding"
				capture.Results[0].Report.Findings = append(capture.Results[0].Report.Findings, finding)
				labels.Decisions = []revieweval.Decision{{CaseID: "zero-division", FindingIndex: 1, IssueID: "zero-count", Reason: "Only later finding adjudicated."}}
				wantMatched, wantPendingIssues = 1, 0
			case "clean-case":
				finding.Path = "label.py"
				capture.Results[3].Report.Findings = []review.Finding{finding}
				labels.Decisions = []revieweval.Decision{{CaseID: "zero-division", FindingIndex: 0, IssueID: "zero-count", Reason: "Matched regression."}}
				wantMatched, wantPendingIssues = 1, 0
			}
			labels.CaptureSHA256 = revieweval.CaptureSHA256(capture)
			report, err := revieweval.Score(capture, &labels)
			if err != nil {
				t.Fatal(err)
			}
			s := report.SeverityCalibration
			if s.MatchedIssues != wantMatched || s.MissedIssues != wantMissed || s.PendingIssues != wantPendingIssues || s.PendingFindings != 1 || s.ExactMatchRate != nil || report.Summary.Recall != nil {
				t.Fatalf("incomplete labels claimed a quality ratio: %+v", s)
			}
			if scenario == "unmatched-issue" && (s.Issues[0].State != "pending" || s.Issues[0].FindingIndex != nil || s.Issues[0].ReportedSeverity != "") {
				t.Fatal("unjudged location was automatically matched")
			}
			if scenario == "earlier-duplicate" {
				if *s.Issues[0].FindingIndex != 1 || s.ExactMatches != 1 {
					t.Fatal("provisional match not traceable to adjudicated finding")
				}
				labels.Decisions = append(labels.Decisions, revieweval.Decision{CaseID: "zero-division", FindingIndex: 0, IssueID: "zero-count", Reason: "Earlier finding now adjudicated."})
				completed, err := revieweval.Score(capture, &labels)
				if err != nil || completed.SeverityCalibration.ExactMatchRate.Value != 0 || completed.SeverityCalibration.UnderstatedIssues != 1 || completed.SeverityCalibration.ExcludedFindings != 1 || *completed.SeverityCalibration.Issues[0].FindingIndex != 0 {
					t.Fatal("completed labels did not select first capture finding", err)
				}
			}
		})
	}
}

func TestSeverityCalibrationPreservesFailureAndEmptyDenominators(t *testing.T) {
	for _, state := range []string{"succeeded", "timeout", "canceled", "invalid_output", "execution_failed", "preparation_failed", "skipped"} {
		t.Run(state, func(t *testing.T) {
			capture := sampleCapture()
			for i := range capture.Results {
				if state == "skipped" {
					capture.Results[i] = revieweval.Result{CaseID: capture.Results[i].CaseID, State: state}
				} else if state != "succeeded" {
					capture.Results[i].State, capture.Results[i].Report = state, nil
				}
			}
			report, err := revieweval.Score(capture, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := report.SeverityCalibration
			if s.ExpectedIssues != 4 || s.MissedIssues != 4 || s.MatchedIssues != 0 || s.PendingIssues != 0 || s.PendingFindings != 0 || s.ExcludedFindings != 0 || s.ExactMatchRate != nil || report.Summary.FalseNegatives != 4 {
				t.Fatalf("empty/failed outputs fabricated severity or hid misses: %+v", s)
			}
			for _, issue := range s.Issues {
				if issue.State != "missed" || issue.FindingIndex != nil || issue.ReportedSeverity != "" {
					t.Fatal("miss acquired a predicted severity")
				}
			}
			for _, row := range s.ConfusionMatrix {
				for _, count := range row {
					if count != 0 {
						t.Fatal("empty matrix acquired a prediction")
					}
				}
			}
		})
	}
}

func TestSeverityCalibrationOrdersAllSupportedReportedSeverities(t *testing.T) {
	for _, example := range []struct {
		severity string
		exact    int
		under    int
		over     int
	}{{"LOW", 0, 1, 0}, {"MEDIUM", 1, 0, 0}, {"HIGH", 0, 0, 1}, {"CRITICAL", 0, 0, 1}} {
		t.Run(example.severity, func(t *testing.T) {
			capture := sampleCapture()
			capture.Results[2].Report.Findings = []review.Finding{{Title: "Index", Description: "Off by one", Severity: example.severity, Confidence: 1, Path: "paging.py", StartLine: 2, EndLine: 2}}
			labels := revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: revieweval.CaptureSHA256(capture), Reviewer: "controlled-test", Decisions: []revieweval.Decision{{CaseID: "upper-bound", FindingIndex: 0, IssueID: "index-equals-length", Reason: "Controlled match."}}}
			report, err := revieweval.Score(capture, &labels)
			if err != nil {
				t.Fatal(err)
			}
			s := report.SeverityCalibration
			if s.ExactMatches != example.exact || s.UnderstatedIssues != example.under || s.OverstatedIssues != example.over || s.ConfusionMatrix["MEDIUM"][example.severity] != 1 {
				t.Fatalf("incorrect ordinal comparison: %+v", s)
			}
		})
	}
}
