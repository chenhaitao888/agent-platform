package review

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidFindings = errors.New("invalid review findings")

const MaxFindingsBytes = 256 << 10

//go:embed pr-review-findings-v1.json
var findingsSchema string

// FindingsSchema returns the output contract supplied to codex exec.
func FindingsSchema() []byte { return []byte(findingsSchema) }

type Finding struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Severity    string  `json:"severity"`
	Confidence  float64 `json:"confidence"`
	Path        string  `json:"path"`
	StartLine   int     `json:"startLine"`
	EndLine     int     `json:"endLine"`
}

type FindingsReport struct {
	SchemaVersion string    `json:"schemaVersion"`
	BaseSHA       string    `json:"baseSha"`
	HeadSHA       string    `json:"headSha"`
	Findings      []Finding `json:"findings"`
}

// ParseFindings binds model output to the commits selected by the platform.
func ParseFindings(content []byte, baseSHA, headSHA string) (FindingsReport, error) {
	if len(content) > MaxFindingsBytes || !utf8.Valid(content) || !validFindingsCommit(baseSHA) || !validFindingsCommit(headSHA) || !validFindingsJSONShape(content) {
		return FindingsReport{}, ErrInvalidFindings
	}
	var report FindingsReport
	if err := json.Unmarshal(content, &report); err != nil {
		return FindingsReport{}, ErrInvalidFindings
	}
	if report.SchemaVersion != "1.0" || report.BaseSHA != baseSHA || report.HeadSHA != headSHA || report.Findings == nil || len(report.Findings) > 50 {
		return FindingsReport{}, ErrInvalidFindings
	}
	unique := make([]Finding, 0, len(report.Findings))
	seen := make(map[Finding]bool)
	for _, finding := range report.Findings {
		if !validFinding(finding) {
			return FindingsReport{}, ErrInvalidFindings
		}
		if !seen[finding] {
			seen[finding] = true
			unique = append(unique, finding)
		}
	}
	report.Findings = unique
	return report, nil
}

func validFinding(finding Finding) bool {
	if strings.TrimSpace(finding.Title) == "" || utf8.RuneCountInString(finding.Title) > 256 || strings.TrimSpace(finding.Description) == "" || utf8.RuneCountInString(finding.Description) > 4096 {
		return false
	}
	switch finding.Severity {
	case "LOW", "MEDIUM", "HIGH", "CRITICAL":
	default:
		return false
	}
	if finding.Confidence < 0 || finding.Confidence > 1 || finding.StartLine < 1 || finding.EndLine < finding.StartLine || finding.EndLine > 2147483647 {
		return false
	}
	if finding.Path == "" || utf8.RuneCountInString(finding.Path) > 1024 || finding.Path == "." || path.Clean(finding.Path) != finding.Path || strings.HasPrefix(finding.Path, "/") || strings.ContainsAny(finding.Path, "\\:") || finding.Path == ".." || strings.HasPrefix(finding.Path, "../") {
		return false
	}
	return strings.IndexFunc(finding.Path, unicode.IsControl) == -1
}

func validFindingsCommit(value string) bool {
	if (len(value) != 40 && len(value) != 64) || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// encoding/json accepts duplicate and case-insensitive field names, and maps
// null numbers to zero. Check the exact, non-null wire shape before decoding.
// The only containers in v1 are the report, its findings array and each finding.
func validFindingsJSONShape(content []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if !readFindingsJSONValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func readFindingsJSONValue(decoder *json.Decoder, depth int) bool {
	token, err := decoder.Token()
	if err != nil || token == nil {
		return false
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return depth == 1 || depth == 3
	}
	switch delimiter {
	case '{':
		if depth != 0 && depth != 2 {
			return false
		}
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] || !findingsJSONFieldAllowed(key, depth) {
				return false
			}
			seen[key] = true
			if !readFindingsJSONValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		wantFields := 4
		if depth == 2 {
			wantFields = 7
		}
		return err == nil && closing == json.Delim('}') && len(seen) == wantFields
	case '[':
		if depth != 1 {
			return false
		}
		for decoder.More() {
			if !readFindingsJSONValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func findingsJSONFieldAllowed(key string, depth int) bool {
	if depth == 0 {
		switch key {
		case "schemaVersion", "baseSha", "headSha", "findings":
			return true
		}
	} else {
		switch key {
		case "title", "description", "severity", "confidence", "path", "startLine", "endLine":
			return true
		}
	}
	return false
}
