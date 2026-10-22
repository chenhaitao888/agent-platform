package revieweval

import "slices"

// SeverityCalibration measures severity only on adjudicated regression matches.
// Missed and unresolved issues stay visible, but do not acquire a made-up
// predicted severity. It does not change detection or serious-recall metrics.
type SeverityCalibration struct {
	Policy            string                    `json:"policy"`
	ExpectedIssues    int                       `json:"expectedIssues"`
	MatchedIssues     int                       `json:"matchedIssues"`
	ExactMatches      int                       `json:"exactMatches"`
	UnderstatedIssues int                       `json:"understatedIssues"`
	OverstatedIssues  int                       `json:"overstatedIssues"`
	MissedIssues      int                       `json:"missedIssues"`
	PendingIssues     int                       `json:"pendingIssues"`
	PendingFindings   int                       `json:"pendingFindings"`
	ExcludedFindings  int                       `json:"excludedFindings"`
	ExactMatchRate    *Rate                     `json:"exactMatchRate"`
	ConfusionMatrix   map[string]map[string]int `json:"confusionMatrix"`
	Issues            []SeverityIssue           `json:"issues"`
}

type SeverityIssue struct {
	CaseID           string `json:"caseId"`
	IssueID          string `json:"issueId"`
	ExpectedSeverity string `json:"expectedSeverity"`
	State            string `json:"state"`
	FindingIndex     *int   `json:"findingIndex,omitempty"`
	ReportedSeverity string `json:"reportedSeverity,omitempty"`
}

var severityLevels = []string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}

func newSeverityCalibration() *SeverityCalibration {
	s := &SeverityCalibration{Policy: "first-adjudicated-match-v1", ConfusionMatrix: map[string]map[string]int{}, Issues: []SeverityIssue{}}
	// Rows are expected severity, columns are reported severity. Explicit zeros
	// distinguish absent observations from absent/unknown severity categories.
	for _, expected := range severityLevels {
		s.ConfusionMatrix[expected] = map[string]int{}
		for _, reported := range severityLevels {
			s.ConfusionMatrix[expected][reported] = 0
		}
	}
	return s
}

// matched comes from the existing detection pass in capture finding order.
// A later duplicate cannot replace the first adjudicated match with a more
// favorable severity; FP (including duplicates) and advisories are excluded.
func (s *SeverityCalibration) addCase(c Case, result Result, metrics Metrics, matched map[string]int) {
	s.PendingFindings += metrics.PendingFindings
	s.ExcludedFindings += metrics.FalsePositives + metrics.Advisories
	for _, issue := range c.Issues {
		s.ExpectedIssues++
		entry := SeverityIssue{CaseID: c.ID, IssueID: issue.ID, ExpectedSeverity: issue.Severity}
		if index, ok := matched[issue.ID]; ok {
			entry.State = "matched"
			entry.FindingIndex = &index
			entry.ReportedSeverity = result.Report.Findings[index].Severity
			s.MatchedIssues++
			s.ConfusionMatrix[issue.Severity][entry.ReportedSeverity]++
			// Both severities are from validated findings or the embedded suite.
			delta := slices.Index(severityLevels, entry.ReportedSeverity) - slices.Index(severityLevels, issue.Severity)
			switch {
			case delta < 0:
				s.UnderstatedIssues++
			case delta > 0:
				s.OverstatedIssues++
			default:
				s.ExactMatches++
			}
		} else if metrics.PendingFindings > 0 {
			// The unjudged finding may match this issue. Follow detection's
			// conservative rule instead of declaring it a confirmed miss.
			entry.State = "pending"
			s.PendingIssues++
		} else {
			entry.State = "missed"
			s.MissedIssues++
		}
		s.Issues = append(s.Issues, entry)
	}
}

func (s *SeverityCalibration) finalize() {
	// Even a pending finding on a clean case means adjudication is incomplete.
	// Keep counts inspectable, but avoid publishing a completed quality ratio.
	if s.PendingFindings == 0 {
		s.ExactMatchRate = rate(s.ExactMatches, s.MatchedIssues)
	}
}
