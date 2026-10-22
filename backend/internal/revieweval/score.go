package revieweval

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"agent-platform/backend/internal/review"
)

var ErrInvalidEvaluation = errors.New("invalid evaluation input")

type Capture struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	SuiteSHA256     string                 `json:"suiteSha256"`
	Evidence        string                 `json:"evidence"`
	StartedAt       time.Time              `json:"startedAt"`
	CaseTimeoutMS   int64                  `json:"caseTimeoutMs"`
	Runtime         review.RuntimeIdentity `json:"runtime"`
	EvaluatorSHA256 string                 `json:"evaluatorSha256,omitempty"`
	Results         []Result               `json:"results"`
}

type Result struct {
	CaseID         string                 `json:"caseId"`
	BaseSHA        string                 `json:"baseSha"`
	HeadSHA        string                 `json:"headSha"`
	PatchSHA256    string                 `json:"patchSha256"`
	State          string                 `json:"state"`
	DurationMS     int64                  `json:"durationMs"`
	Report         *review.FindingsReport `json:"report"`
	Usage          *review.ModelUsage     `json:"usage,omitempty"`
	DiagnosticCode string                 `json:"diagnosticCode,omitempty"`
}

type Adjudications struct {
	SchemaVersion string     `json:"schemaVersion"`
	CaptureSHA256 string     `json:"captureSha256"`
	Reviewer      string     `json:"reviewer"`
	ReviewerKind  string     `json:"reviewerKind,omitempty"`
	Rubric        string     `json:"rubric,omitempty"`
	Decisions     []Decision `json:"decisions"`
}

type Decision struct {
	CaseID       string `json:"caseId"`
	FindingIndex int    `json:"findingIndex"`
	// Empty means a false positive unless Advisory is explicitly supplied.
	// Nonempty must name a ground truth issue and cannot also be an advisory.
	IssueID  string `json:"issueId"`
	Reason   string `json:"reason"`
	Advisory string `json:"advisory,omitempty"`
}

type Rate struct {
	Value        float64 `json:"value"`
	Numerator    int     `json:"numerator"`
	Denominator  int     `json:"denominator"`
	Wilson95Low  float64 `json:"wilson95Low"`
	Wilson95High float64 `json:"wilson95High"`
}

type Metrics struct {
	Cases           int            `json:"cases"`
	States          map[string]int `json:"states"`
	ExpectedIssues  int            `json:"expectedIssues"`
	SeriousIssues   int            `json:"seriousIssues"`
	TruePositives   int            `json:"truePositives"`
	FalsePositives  int            `json:"falsePositives"`
	Advisories      int            `json:"advisories"`
	FalseNegatives  int            `json:"falseNegatives"`
	SeriousFound    int            `json:"seriousFound"`
	PendingFindings int            `json:"pendingFindings"`
	Precision       *Rate          `json:"precision"`
	Recall          *Rate          `json:"recall"`
	SeriousRecall   *Rate          `json:"seriousRecall"`
	TotalDurationMS int64          `json:"totalDurationMs"`
	DurationSamples int            `json:"durationSamples"`
	P50DurationMS   int64          `json:"p50DurationMs"`
	P95DurationMS   int64          `json:"p95DurationMs"`
}

type CaseScore struct {
	CaseID         string  `json:"caseId"`
	Category       string  `json:"category"`
	Metrics        Metrics `json:"metrics"`
	DiagnosticCode string  `json:"diagnosticCode,omitempty"`
}

type Report struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	SuiteSHA256         string                 `json:"suiteSha256"`
	CaptureSHA256       string                 `json:"captureSha256"`
	AdjudicationsSHA256 string                 `json:"adjudicationsSha256,omitempty"`
	Evidence            string                 `json:"evidence"`
	Runtime             review.RuntimeIdentity `json:"runtime"`
	EvaluatorSHA256     string                 `json:"evaluatorSha256,omitempty"`
	ScorerSHA256        string                 `json:"scorerSha256,omitempty"`
	AdjudicationKind    string                 `json:"adjudicationKind"`
	Rubric              string                 `json:"rubric"`
	Usage               *review.ModelUsage     `json:"usage"`
	Summary             Metrics                `json:"summary"`
	Categories          map[string]Metrics     `json:"categories"`
	Cases               []CaseScore            `json:"cases"`
	TokenUsage          *int64                 `json:"tokenUsage"`
	CostUSD             *float64               `json:"costUsd"`
	EstimatedCostUSD    *float64               `json:"estimatedCostUsd"`
	Pricing             *Pricing               `json:"pricing,omitempty"`
	SafetyAssessment    string                 `json:"safetyAssessment"`
}

// CaptureSHA256 binds annotations to the complete canonical capture, including
// the actual finding order, refs, runtime, and durations; it is not a signature.
func CaptureSHA256(c Capture) string {
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return digest(data)
}

func Score(capture Capture, labels *Adjudications) (Report, error) {
	return ScoreWithPricing(capture, labels, nil)
}

func ScoreWithPricing(capture Capture, labels *Adjudications, pricing *Pricing) (Report, error) {
	suite := DefaultSuite()
	if err := validateCapture(capture, suite); err != nil {
		return Report{}, err
	}
	decisions, err := validateDecisions(capture, suite, labels)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: "1.0", SuiteSHA256: capture.SuiteSHA256, CaptureSHA256: CaptureSHA256(capture), Evidence: capture.Evidence, Runtime: capture.Runtime, Categories: map[string]Metrics{}, SafetyAssessment: "not_evaluated"}
	report.EvaluatorSHA256 = capture.EvaluatorSHA256
	report.AdjudicationKind = "pending"
	report.Rubric = "closed-issues-v1"
	if labels != nil {
		if labels.Rubric != "" {
			report.Rubric = labels.Rubric
		}
		report.AdjudicationKind = labels.ReviewerKind
		if report.AdjudicationKind == "" {
			report.AdjudicationKind = "unspecified"
		}
		data, _ := json.Marshal(labels)
		report.AdjudicationsSHA256 = digest(data)
	}
	categoryDurations := map[string][]int64{}
	var durations []int64
	for i, c := range suite.Cases {
		result := capture.Results[i]
		m := Metrics{Cases: 1, States: map[string]int{result.State: 1}, ExpectedIssues: len(c.Issues), TotalDurationMS: result.DurationMS}
		matched := map[string]bool{}
		if result.Report != nil {
			for index := range result.Report.Findings {
				decision, ok := decisions[c.ID][index]
				if !ok {
					m.PendingFindings++
					continue
				}
				if decision.Advisory != "" {
					m.Advisories++
				} else if decision.IssueID == "" || matched[decision.IssueID] {
					m.FalsePositives++
				} else {
					matched[decision.IssueID] = true
					m.TruePositives++
				}
			}
		}
		for _, issue := range c.Issues {
			serious := issue.Severity == "HIGH" || issue.Severity == "CRITICAL"
			if serious {
				m.SeriousIssues++
				if matched[issue.ID] {
					m.SeriousFound++
				}
			}
			// While judgments are pending an unmatched issue is unresolved, not
			// yet a confirmed miss. Failed/skipped executions have no findings.
			if !matched[issue.ID] && m.PendingFindings == 0 {
				m.FalseNegatives++
			}
		}
		var measured []int64
		if result.State != "skipped" {
			measured = []int64{result.DurationMS}
		}
		finalize(&m, measured)
		report.Cases = append(report.Cases, CaseScore{CaseID: c.ID, Category: c.Category, Metrics: m, DiagnosticCode: result.DiagnosticCode})
		addMetrics(&report.Summary, m)
		category := report.Categories[c.Category]
		addMetrics(&category, m)
		report.Categories[c.Category] = category
		durations = append(durations, measured...)
		categoryDurations[c.Category] = append(categoryDurations[c.Category], measured...)
	}
	finalize(&report.Summary, durations)
	for category, m := range report.Categories {
		finalize(&m, categoryDurations[category])
		report.Categories[category] = m
	}
	if err := scoreUsage(&report, capture, pricing); err != nil {
		return Report{}, err
	}
	return report, nil
}

func validateCapture(c Capture, suite Suite) error {
	if c.EvaluatorSHA256 != "" && !validHex(c.EvaluatorSHA256, 64) {
		return ErrInvalidEvaluation
	}
	if c.SchemaVersion != "1.0" || c.SuiteSHA256 != SuiteSHA256() || (c.Evidence != "controlled" && c.Evidence != "deployment") || c.StartedAt.IsZero() || c.CaseTimeoutMS < 1 || c.CaseTimeoutMS > 120000 || len(c.Results) != len(suite.Cases) || c.Runtime.Model == "" || c.Runtime.CodexVersion == "" || !validHex(c.Runtime.BinarySHA256, 64) {
		return ErrInvalidEvaluation
	}
	for i, r := range c.Results {
		if r.DiagnosticCode != "" && (r.State != "invalid_output" || !review.ValidOutputDiagnostic(r.DiagnosticCode)) {
			return ErrInvalidEvaluation
		}
		if r.CaseID != suite.Cases[i].ID || r.DurationMS < 0 || r.DurationMS > 24*60*60*1000 {
			return ErrInvalidEvaluation
		}
		switch r.State {
		case "succeeded":
			if r.Report == nil {
				return ErrInvalidEvaluation
			}
			data, err := json.Marshal(r.Report)
			if err != nil {
				return ErrInvalidEvaluation
			}
			parsed, err := review.ParseFindings(data, r.BaseSHA, r.HeadSHA)
			if err != nil || len(parsed.Findings) != len(r.Report.Findings) {
				return ErrInvalidEvaluation
			}
		case "timeout", "canceled", "invalid_output", "execution_failed", "preparation_failed", "skipped":
			if r.Report != nil {
				return ErrInvalidEvaluation
			}
		default:
			return ErrInvalidEvaluation
		}
		// Preparation can be canceled before either commit or the patch exists.
		// Preserve this as a timeout/cancellation without inventing input pins.
		requirePins := r.State == "succeeded" || r.State == "invalid_output" || r.State == "execution_failed" || r.Usage != nil
		if (requirePins || r.BaseSHA != "") && !validHex(r.BaseSHA, 40) {
			return ErrInvalidEvaluation
		}
		if (requirePins || r.HeadSHA != "") && !validHex(r.HeadSHA, 40) {
			return ErrInvalidEvaluation
		}
		if (requirePins || r.PatchSHA256 != "") && !validHex(r.PatchSHA256, 64) {
			return ErrInvalidEvaluation
		}
		if r.State == "skipped" && (r.DurationMS != 0 || r.BaseSHA != "" || r.HeadSHA != "" || r.PatchSHA256 != "") {
			return ErrInvalidEvaluation
		}
	}
	return nil
}

func validateDecisions(capture Capture, suite Suite, labels *Adjudications) (map[string]map[int]Decision, error) {
	indexed := map[string]map[int]Decision{}
	if labels == nil {
		return indexed, nil
	}
	if labels.SchemaVersion != "1.0" || labels.CaptureSHA256 != CaptureSHA256(capture) || strings.TrimSpace(labels.Reviewer) == "" || len(labels.Reviewer) > 256 {
		return nil, ErrInvalidEvaluation
	}
	if labels.ReviewerKind != "" && labels.ReviewerKind != "human" && labels.ReviewerKind != "assistant" {
		return nil, ErrInvalidEvaluation
	}
	if labels.Rubric != "" && labels.Rubric != "closed-issues-v1" && labels.Rubric != "regression-with-advisories-v1" {
		return nil, ErrInvalidEvaluation
	}
	for _, decision := range labels.Decisions {
		caseIndex := -1
		for i, c := range suite.Cases {
			if c.ID == decision.CaseID {
				caseIndex = i
				break
			}
		}
		if caseIndex < 0 || decision.FindingIndex < 0 || strings.TrimSpace(decision.Reason) == "" || len(decision.Reason) > 4096 {
			return nil, ErrInvalidEvaluation
		}
		r := capture.Results[caseIndex]
		if r.Report == nil || decision.FindingIndex >= len(r.Report.Findings) {
			return nil, ErrInvalidEvaluation
		}
		if decision.Advisory != "" && !validAdvisory(labels.Rubric, decision, suite.Cases[caseIndex], r.Report.Findings[decision.FindingIndex]) {
			return nil, ErrInvalidEvaluation
		}
		knownIssue := decision.IssueID == ""
		for _, issue := range suite.Cases[caseIndex].Issues {
			if decision.IssueID == issue.ID {
				knownIssue = true
			}
		}
		if !knownIssue {
			return nil, ErrInvalidEvaluation
		}
		if indexed[decision.CaseID] == nil {
			indexed[decision.CaseID] = map[int]Decision{}
		}
		if _, exists := indexed[decision.CaseID][decision.FindingIndex]; exists {
			return nil, ErrInvalidEvaluation
		}
		indexed[decision.CaseID][decision.FindingIndex] = decision
	}
	return indexed, nil
}

func validHex(s string, size int) bool {
	_, err := hex.DecodeString(s)
	return err == nil && len(s) == size && strings.ToLower(s) == s
}

func rate(n, d int) *Rate {
	if d == 0 {
		return nil
	}
	p, total, z := float64(n)/float64(d), float64(d), 1.959963984540054
	den := 1 + z*z/total
	center := (p + z*z/(2*total)) / den
	half := z * math.Sqrt(p*(1-p)/total+z*z/(4*total*total)) / den
	return &Rate{Value: p, Numerator: n, Denominator: d, Wilson95Low: math.Max(0, center-half), Wilson95High: math.Min(1, center+half)}
}
