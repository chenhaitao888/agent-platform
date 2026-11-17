package planning

import (
	"context"
	_ "embed"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"agent-platform/backend/internal/strictjson"
)

const MaxModelAnalysisBytes = 256 << 10

//go:embed model-candidates-v1.json
var modelSchema []byte

func ModelSchema() []byte { return slices.Clone(modelSchema) }

// ResourceContext intentionally excludes values, secret references, addresses,
// credentials and connector IDs. It is a projection of a validated read.
type ResourceContext struct {
	Kind    string         `json:"kind"`
	Status  string         `json:"status"`
	Version string         `json:"version,omitempty"`
	Items   []ResourceItem `json:"items"`
}
type ResourceItem struct {
	Key   string `json:"key"`
	Type  string `json:"type,omitempty"`
	State string `json:"state,omitempty"`
}
type AnalysisQuestion struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type ModelIdentity struct {
	Model       string `json:"model"`
	RuntimeHash string `json:"runtimeHash"`
}
type AnalysisTrace struct {
	SchemaVersion string        `json:"schemaVersion"`
	InputHash     string        `json:"inputHash"`
	OutputHash    string        `json:"outputHash"`
	Identity      ModelIdentity `json:"identity"`
}
type CandidateRunner interface {
	RunCandidates(context.Context, AnalysisInput) ([]byte, error)
	CandidateIdentity() ModelIdentity
}
type ModelAnalyzer struct{ runner CandidateRunner }

func NewModelAnalyzer(runner CandidateRunner) (*ModelAnalyzer, error) {
	if runner == nil || runner.CandidateIdentity().Model == "" || !digestPattern.MatchString(runner.CandidateIdentity().RuntimeHash) {
		return nil, ErrInvalid
	}
	return &ModelAnalyzer{runner: runner}, nil
}
func (a *ModelAnalyzer) Version() string           { return "model-and-literal.v1" }
func (a *ModelAnalyzer) UsesPlatformContext() bool { return true }
func (a *ModelAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (Analysis, error) {
	if err := ValidateModelInput(in); err != nil {
		return Analysis{}, err
	}
	data, err := a.runner.RunCandidates(ctx, in)
	if err != nil {
		return Analysis{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return Analysis{}, ctx.Err()
	}
	result, err := ParseModelAnalysis(data, in)
	if err != nil {
		return Analysis{}, err
	}
	result.Trace.Identity = a.runner.CandidateIdentity()
	// A model omission cannot erase a deterministic literal reference.
	literal, err := NewDiffAnalyzer().Analyze(ctx, in)
	if err != nil {
		return Analysis{}, err
	}
	for _, c := range literal.Candidates {
		index := slices.IndexFunc(result.Candidates, func(v Candidate) bool { return c.Kind == v.Kind && c.Key == v.Key })
		if index < 0 {
			c.Origin = "literal"
			result.Candidates = append(result.Candidates, c)
			continue
		}
		candidate := &result.Candidates[index]
		candidate.Origin = "model+literal"
		for _, e := range c.Evidence {
			if !slices.Contains(candidate.Evidence, e) && len(candidate.Evidence) < 10 {
				candidate.Evidence = append(candidate.Evidence, e)
			}
		}
	}
	if len(result.Candidates) > 100 {
		return Analysis{}, ErrInvalid
	}
	slices.SortFunc(result.Candidates, func(a, b Candidate) int { return strings.Compare(a.Kind+":"+a.Key, b.Kind+":"+b.Key) })
	return result, nil
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func ValidateModelInput(in AnalysisInput) error {
	if !ValidIdentifier(in.ServiceID) || !digestPattern.MatchString(in.SnapshotHash) || !commitPattern.MatchString(in.BaseSHA) || !commitPattern.MatchString(in.HeadSHA) || len(in.Patch) > 1<<20 || !utf8.ValidString(in.Patch) || len(in.Goal) > 16384 || !utf8.ValidString(in.Goal) || !ValidIdentifier(in.Environment) || len(in.Resources) > 2 {
		return ErrInvalid
	}
	for _, r := range in.Resources {
		if (r.Kind != "config" && r.Kind != "experiment") || !slices.Contains([]string{"AVAILABLE", "UNAVAILABLE", "UNBOUND"}, r.Status) || len(r.Items) > 1000 {
			return ErrInvalid
		}
		for _, item := range r.Items {
			if !ValidIdentifier(item.Key) || len(item.Type) > 32 || len(item.State) > 32 {
				return ErrInvalid
			}
		}
	}
	return nil
}

type modelOutput struct {
	SchemaVersion string             `json:"schemaVersion"`
	InputHash     string             `json:"inputHash"`
	SnapshotHash  string             `json:"snapshotHash"`
	ServiceID     string             `json:"serviceId"`
	BaseSHA       string             `json:"baseSha"`
	HeadSHA       string             `json:"headSha"`
	Candidates    []modelCandidate   `json:"candidates"`
	Questions     []AnalysisQuestion `json:"questions"`
}
type modelCandidate struct {
	Kind     string          `json:"kind"`
	Key      string          `json:"key"`
	Reason   string          `json:"reason"`
	Evidence []modelEvidence `json:"evidence"`
}
type modelEvidence struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// ParseModelAnalysis validates the untrusted output against actual added lines,
// not just a syntactically plausible path/line or a model-provided SHA.
func ParseModelAnalysis(data []byte, in AnalysisInput) (Analysis, error) {
	var output modelOutput
	if ValidateModelInput(in) != nil || len(data) > MaxModelAnalysisBytes || !utf8.Valid(data) || strictjson.Decode(data, &output) != nil || output.SchemaVersion != "model-candidates.v1" || output.InputHash != Hash(in) || output.SnapshotHash != in.SnapshotHash || output.ServiceID != in.ServiceID || output.BaseSHA != in.BaseSHA || output.HeadSHA != in.HeadSHA || output.Candidates == nil || output.Questions == nil || len(output.Candidates) > 100 || len(output.Questions) > 20 {
		return Analysis{}, ErrInvalid
	}
	lines := addedLines(in.Patch)
	result := Analysis{SchemaVersion: "change-candidates.v1", SnapshotHash: in.SnapshotHash, ServiceID: in.ServiceID, HeadSHA: in.HeadSHA, Candidates: []Candidate{}, Questions: output.Questions, Trace: &AnalysisTrace{SchemaVersion: "model-candidates.v1", InputHash: Hash(in), OutputHash: Hash(output)}}
	seen := map[string]bool{}
	for _, c := range output.Candidates {
		if (c.Kind != "config" && c.Kind != "experiment") || !ValidIdentifier(c.Key) || seen[c.Kind+":"+c.Key] || !validReason(c.Reason) || len(c.Evidence) == 0 || len(c.Evidence) > 10 {
			return Analysis{}, ErrInvalid
		}
		seen[c.Kind+":"+c.Key] = true
		candidate := Candidate{Kind: c.Kind, Key: c.Key, Reason: c.Reason, Origin: "model", Evidence: []Evidence{}}
		for _, e := range c.Evidence {
			if !lines[e] || e.Line <= 0 || e.Line > 2147483647 || len(e.Path) > 256 || path.Clean(e.Path) != e.Path || strings.HasPrefix(e.Path, "/") || strings.HasPrefix(e.Path, "..") || strings.ContainsAny(e.Path, "\\\x00\t\n") {
				return Analysis{}, ErrInvalid
			}
			evidence := Evidence{Source: "fixed-diff", ServiceID: in.ServiceID, HeadSHA: in.HeadSHA, Path: e.Path, Line: e.Line}
			if slices.Contains(candidate.Evidence, evidence) {
				return Analysis{}, ErrInvalid
			}
			candidate.Evidence = append(candidate.Evidence, evidence)
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	for _, q := range result.Questions {
		if (q.Kind != "config" && q.Kind != "experiment") || !validReason(q.Reason) {
			return Analysis{}, ErrInvalid
		}
	}
	return result, nil
}
func validReason(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 1024 && !strings.ContainsAny(s, "\x00\r")
}

func addedLines(patch string) map[modelEvidence]bool {
	result := map[modelEvidence]bool{}
	file := ""
	line := 0
	for _, raw := range strings.Split(patch, "\n") {
		if strings.HasPrefix(raw, "diff --git ") {
			file = ""
			line = 0
			continue
		}
		if strings.HasPrefix(raw, "+++ ") {
			file = strings.TrimPrefix(raw, "+++ b/")
			line = 0
			continue
		}
		if match := hunkPattern.FindStringSubmatch(raw); match != nil {
			line, _ = strconv.Atoi(match[1])
			continue
		}
		if line == 0 || file == "" || len(raw) == 0 {
			continue
		}
		if raw[0] == '+' {
			result[modelEvidence{Path: file, Line: line}] = true
		}
		if raw[0] == '+' || raw[0] == ' ' {
			line++
		}
	}
	return result
}
