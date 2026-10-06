package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	findingsBaseSHA  = "1111111111111111111111111111111111111111"
	findingsHeadSHA  = "2222222222222222222222222222222222222222"
	validFindingJSON = `{"title":"Check the nil input","description":"This path dereferences the input before checking it.","severity":"HIGH","confidence":0.95,"path":"src/review.go","startLine":12,"endLine":14}`
)

func TestParseFindingsAcceptsAnEmptyReviewForTheFixedCommits(t *testing.T) {
	report, err := ParseFindings([]byte(`{
		"schemaVersion":"1.0",
		"baseSha":"1111111111111111111111111111111111111111",
		"headSha":"2222222222222222222222222222222222222222",
		"findings":[]
	}`), findingsBaseSHA, findingsHeadSHA)
	if err != nil {
		t.Fatalf("accept an empty but valid review: %v", err)
	}
	if report.SchemaVersion != "1.0" || report.BaseSHA != findingsBaseSHA || report.HeadSHA != findingsHeadSHA || report.Findings == nil || len(report.Findings) != 0 {
		t.Fatalf("unexpected empty review: %+v", report)
	}
}

func TestParseFindingsRejectsAPathOutsideTheRepository(t *testing.T) {
	content := `{"schemaVersion":"1.0","baseSha":"` + findingsBaseSHA + `","headSha":"` + findingsHeadSHA + `","findings":[
		{"title":"Check the nil input","description":"This path dereferences the input before checking it.","severity":"HIGH","confidence":0.95,"path":"../private.txt","startLine":12,"endLine":14}
	]}`
	if _, err := ParseFindings([]byte(content), findingsBaseSHA, findingsHeadSHA); !errors.Is(err, ErrInvalidFindings) {
		t.Fatalf("repository traversal must be rejected, got %v", err)
	}
}

func TestParseFindingsRequiresConfidenceEvenWhenZeroIsValid(t *testing.T) {
	content := `{"schemaVersion":"1.0","baseSha":"` + findingsBaseSHA + `","headSha":"` + findingsHeadSHA + `","findings":[` + strings.Replace(validFindingJSON, `"confidence":0.95,`, "", 1) + `]}`
	if _, err := ParseFindings([]byte(content), findingsBaseSHA, findingsHeadSHA); !errors.Is(err, ErrInvalidFindings) {
		t.Fatalf("a missing confidence must not become a valid zero score, got %v", err)
	}
}

func TestParseFindingsRejectsAnInvalidLocationWithoutPartialResults(t *testing.T) {
	invalid := strings.Replace(validFindingJSON, `"endLine":14`, `"endLine":11`, 1)
	content := `{"schemaVersion":"1.0","baseSha":"` + findingsBaseSHA + `","headSha":"` + findingsHeadSHA + `","findings":[` + validFindingJSON + `,` + invalid + `]}`
	report, err := ParseFindings([]byte(content), findingsBaseSHA, findingsHeadSHA)
	if !errors.Is(err, ErrInvalidFindings) || len(report.Findings) != 0 {
		t.Fatalf("an invalid range must reject the entire report, got %+v, %v", report, err)
	}
}

func TestParseFindingsRejectsInvalidReports(t *testing.T) {
	valid := findingsReportJSONForTest(validFindingJSON)
	cases := []struct {
		name    string
		content string
	}{
		{"wrong base", strings.Replace(valid, findingsBaseSHA, strings.Repeat("3", 40), 1)},
		{"wrong head", strings.Replace(valid, findingsHeadSHA, strings.Repeat("3", 40), 1)},
		{"unsupported version", strings.Replace(valid, `"1.0"`, `"2.0"`, 1)},
		{"missing findings", strings.Replace(valid, `,"findings":[`+validFindingJSON+`]`, "", 1)},
		{"null findings", strings.Replace(valid, `[`+validFindingJSON+`]`, "null", 1)},
		{"null item", findingsReportJSONForTest("null")},
		{"unknown field", strings.Replace(valid, `"schemaVersion"`, `"publish":true,"schemaVersion"`, 1)},
		{"unknown finding field", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"title"`, `"command":"publish","title"`, 1))},
		{"duplicate root field", strings.Replace(valid, `"schemaVersion"`, `"schemaVersion":"2.0","schemaVersion"`, 1)},
		{"duplicate finding field", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"confidence"`, `"confidence":0.1,"confidence"`, 1))},
		{"blank title", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"Check the nil input"`, `"  "`, 1))},
		{"blank description", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"This path dereferences the input before checking it."`, `" "`, 1))},
		{"unknown severity", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"HIGH"`, `"URGENT"`, 1))},
		{"negative confidence", findingsReportJSONForTest(strings.Replace(validFindingJSON, "0.95", "-0.1", 1))},
		{"confidence above one", findingsReportJSONForTest(strings.Replace(validFindingJSON, "0.95", "1.1", 1))},
		{"null confidence", findingsReportJSONForTest(strings.Replace(validFindingJSON, "0.95", "null", 1))},
		{"absolute path", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"src/review.go"`, `"/etc/passwd"`, 1))},
		{"windows path", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"src/review.go"`, `"C:\\private.txt"`, 1))},
		{"unclean path", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"src/review.go"`, `"src/../private.txt"`, 1))},
		{"control in path", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"src/review.go"`, `"src/\u0000review.go"`, 1))},
		{"zero line", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"startLine":12`, `"startLine":0`, 1))},
		{"fractional line", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"startLine":12`, `"startLine":1.5`, 1))},
		{"line exceeds contract", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"endLine":14`, `"endLine":2147483648`, 1))},
		{"title exceeds limit", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"Check the nil input"`, `"`+strings.Repeat("题", 257)+`"`, 1))},
		{"description exceeds limit", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"This path dereferences the input before checking it."`, `"`+strings.Repeat("a", 4097)+`"`, 1))},
		{"path exceeds limit", findingsReportJSONForTest(strings.Replace(validFindingJSON, `"src/review.go"`, `"`+strings.Repeat("a", 1025)+`"`, 1))},
		{"too many findings before deduplication", findingsReportJSONForTest(strings.TrimSuffix(strings.Repeat(validFindingJSON+",", 51), ","))},
		{"oversized payload", valid + strings.Repeat(" ", 256<<10)},
		{"trailing JSON", valid + "{}"},
		{"invalid UTF-8", strings.Replace(valid, "Check the nil input", string([]byte{0xff}), 1)},
		{"malformed JSON", "{"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			report, err := ParseFindings([]byte(test.content), findingsBaseSHA, findingsHeadSHA)
			if !errors.Is(err, ErrInvalidFindings) || report.Findings != nil {
				t.Fatalf("invalid report must return no usable results: findings=%d, error=%v", len(report.Findings), err)
			}
		})
	}
}

func TestParseFindingsRequiresValidPlatformCommits(t *testing.T) {
	for _, invalid := range []string{"", "master", strings.Repeat("z", 40)} {
		t.Run(fmt.Sprintf("%q", invalid), func(t *testing.T) {
			content := `{"schemaVersion":"1.0","baseSha":` + fmt.Sprintf("%q", invalid) + `,"headSha":"` + findingsHeadSHA + `","findings":[]}`
			if _, err := ParseFindings([]byte(content), invalid, findingsHeadSHA); !errors.Is(err, ErrInvalidFindings) {
				t.Fatalf("invalid platform commit must fail closed: %v", err)
			}
		})
	}
}

func TestParseFindingsAcceptsSeverityAndConfidenceBoundaries(t *testing.T) {
	for _, severity := range []string{"LOW", "MEDIUM", "HIGH", "CRITICAL"} {
		for _, confidence := range []string{"0", "1"} {
			t.Run(severity+"/"+confidence, func(t *testing.T) {
				finding := strings.Replace(strings.Replace(validFindingJSON, `"HIGH"`, `"`+severity+`"`, 1), "0.95", confidence, 1)
				report, err := ParseFindings([]byte(findingsReportJSONForTest(finding)), findingsBaseSHA, findingsHeadSHA)
				if err != nil || len(report.Findings) != 1 || report.Findings[0].Severity != severity {
					t.Fatalf("valid severity/confidence boundary rejected: %+v, %v", report, err)
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParseFindings(encoded, findingsBaseSHA, findingsHeadSHA); err != nil {
					t.Fatalf("validated output must round trip through the public contract: %v", err)
				}
			})
		}
	}
}

func TestParseFindingsDeduplicatesExactFindingsInOriginalOrder(t *testing.T) {
	different := strings.Replace(validFindingJSON, "Check the nil input", "Check the second branch", 1)
	report, err := ParseFindings([]byte(findingsReportJSONForTest(validFindingJSON+","+different+","+validFindingJSON)), findingsBaseSHA, findingsHeadSHA)
	if err != nil || len(report.Findings) != 2 || report.Findings[0].Title != "Check the nil input" || report.Findings[1].Title != "Check the second branch" {
		t.Fatalf("exact duplicates must be removed while preserving different findings and order: count=%d, error=%v", len(report.Findings), err)
	}
}

func TestParseFindingsRequiresEveryWireField(t *testing.T) {
	for _, field := range []string{"schemaVersion", "baseSha", "headSha", "findings"} {
		for _, absent := range []bool{true, false} {
			t.Run(fmt.Sprintf("report/%s/missing=%t", field, absent), func(t *testing.T) {
				var document map[string]any
				if err := json.Unmarshal([]byte(findingsReportJSONForTest(validFindingJSON)), &document); err != nil {
					t.Fatal(err)
				}
				if absent {
					delete(document, field)
				} else {
					document[field] = nil
				}
				content, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParseFindings(content, findingsBaseSHA, findingsHeadSHA); !errors.Is(err, ErrInvalidFindings) {
					t.Fatalf("missing/null %s must be rejected: %v", field, err)
				}
			})
		}
	}
	for _, field := range []string{"title", "description", "severity", "confidence", "path", "startLine", "endLine"} {
		for _, absent := range []bool{true, false} {
			t.Run(fmt.Sprintf("finding/%s/missing=%t", field, absent), func(t *testing.T) {
				var finding map[string]any
				if err := json.Unmarshal([]byte(validFindingJSON), &finding); err != nil {
					t.Fatal(err)
				}
				if absent {
					delete(finding, field)
				} else {
					finding[field] = nil
				}
				content, err := json.Marshal(finding)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParseFindings([]byte(findingsReportJSONForTest(string(content))), findingsBaseSHA, findingsHeadSHA); !errors.Is(err, ErrInvalidFindings) {
					t.Fatalf("missing/null %s must be rejected: %v", field, err)
				}
			})
		}
	}
}

func TestParseFindingsAcceptsContractLimits(t *testing.T) {
	var items []string
	for index := range 50 {
		items = append(items, strings.Replace(validFindingJSON, "Check the nil input", fmt.Sprintf("Finding %d", index), 1))
	}
	report, err := ParseFindings([]byte(findingsReportJSONForTest(strings.Join(items, ","))), findingsBaseSHA, findingsHeadSHA)
	if err != nil || len(report.Findings) != 50 {
		t.Fatalf("50 distinct findings should be valid: count=%d, error=%v", len(report.Findings), err)
	}
	content := findingsReportJSONForTest("")
	content += strings.Repeat(" ", (256<<10)-len(content))
	if _, err := ParseFindings([]byte(content), findingsBaseSHA, findingsHeadSHA); err != nil {
		t.Fatalf("payload at the byte limit should be valid: %v", err)
	}
	boundaryFinding := strings.Replace(validFindingJSON, "Check the nil input", strings.Repeat("题", 256), 1)
	if _, err := ParseFindings([]byte(findingsReportJSONForTest(boundaryFinding)), findingsBaseSHA, findingsHeadSHA); err != nil {
		t.Fatalf("title limit counts Unicode characters, not UTF-8 bytes: %v", err)
	}
	base, head := strings.Repeat("a", 64), strings.Repeat("b", 64)
	content = strings.ReplaceAll(strings.ReplaceAll(findingsReportJSONForTest(""), findingsBaseSHA, base), findingsHeadSHA, head)
	if _, err := ParseFindings([]byte(content), base, head); err != nil {
		t.Fatalf("64-character immutable commit IDs should be valid: %v", err)
	}
}

func TestParseFindingsReadsVersionedContractFixtures(t *testing.T) {
	for _, test := range []struct {
		file  string
		count int
	}{{"testdata/findings-empty.json", 0}, {"testdata/findings-valid.json", 1}} {
		t.Run(test.file, func(t *testing.T) {
			content, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ParseFindings(content, findingsBaseSHA, findingsHeadSHA)
			if err != nil || len(report.Findings) != test.count {
				t.Fatalf("contract fixture rejected: count=%d, error=%v", len(report.Findings), err)
			}
		})
	}
}

func findingsReportJSONForTest(items string) string {
	return `{"schemaVersion":"1.0","baseSha":"` + findingsBaseSHA + `","headSha":"` + findingsHeadSHA + `","findings":[` + items + `]}`
}
