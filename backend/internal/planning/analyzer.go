package planning

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// DiffAnalyzer recognizes literal calls in the reference integration contract.
// It never guesses business values or treats an empty scan as complete coverage.
// Another versioned Analyzer can replace it without changing plan authorization.
type DiffAnalyzer struct{}

func NewDiffAnalyzer() DiffAnalyzer  { return DiffAnalyzer{} }
func (DiffAnalyzer) Version() string { return "literal-calls.v1" }

var callPattern = regexp.MustCompile(`\b(config\.(?:Get|GetString|GetBool|GetInt|get)|experiments\.(?:Variant|variant))\(\s*["']([a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127})["']\s*[,)]`)
var hunkPattern = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

func (DiffAnalyzer) Analyze(ctx context.Context, in AnalysisInput) (Analysis, error) {
	out := Analysis{SchemaVersion: "change-candidates.v1", SnapshotHash: in.SnapshotHash, ServiceID: in.ServiceID, HeadSHA: in.HeadSHA, Candidates: []Candidate{}}
	if len(in.Patch) > 1<<20 {
		return Analysis{}, ErrInvalid
	}
	file := ""
	line := 0
	seen := map[string]int{}
	for _, raw := range strings.Split(in.Patch, "\n") {
		if ctx.Err() != nil {
			return Analysis{}, ctx.Err()
		}
		if strings.HasPrefix(raw, "+++ ") {
			file = ""
			candidate := strings.TrimPrefix(raw, "+++ b/")
			if len(candidate) <= 256 && path.Clean(candidate) == candidate && !strings.HasPrefix(candidate, "/") && !strings.HasPrefix(candidate, "../") && !strings.ContainsAny(candidate, "\"\\\x00\t") {
				file = candidate
			}
			continue
		}
		if match := hunkPattern.FindStringSubmatch(raw); match != nil {
			line, _ = strconv.Atoi(match[1])
			continue
		}
		if strings.HasPrefix(raw, "diff --git ") {
			file = ""
			line = 0
			continue
		}
		if file == "" || line == 0 || len(raw) == 0 {
			continue
		}
		if raw[0] == '+' {
			code := strings.TrimSpace(raw[1:])
			if !strings.HasPrefix(code, "//") && !strings.HasPrefix(code, "#") && !strings.HasPrefix(code, "/*") && !strings.HasPrefix(code, "*") {
				for _, match := range callPattern.FindAllStringSubmatch(code, -1) {
					kind := "config"
					if strings.HasPrefix(match[1], "experiments.") {
						kind = "experiment"
					}
					if !ValidIdentifier(match[2]) {
						continue
					}
					key := kind + ":" + match[2]
					evidence := Evidence{Source: "fixed-diff", ServiceID: in.ServiceID, HeadSHA: in.HeadSHA, Path: file, Line: line}
					if i, ok := seen[key]; ok {
						if len(out.Candidates[i].Evidence) < 10 {
							out.Candidates[i].Evidence = append(out.Candidates[i].Evidence, evidence)
						}
					} else {
						seen[key] = len(out.Candidates)
						out.Candidates = append(out.Candidates, Candidate{Kind: kind, Key: match[2], Evidence: []Evidence{evidence}})
					}
					if len(out.Candidates) > 100 {
						return Analysis{}, ErrInvalid
					}
				}
			}
		}
		if raw[0] == '+' || raw[0] == ' ' {
			line++
		}
	}
	return out, nil
}
