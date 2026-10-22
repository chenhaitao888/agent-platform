package review_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"agent-platform/backend/internal/review"
)

func TestRejectedFindingsHaveBoundedNonSensitiveDiagnostics(t *testing.T) {
	data, err := os.ReadFile("testdata/findings-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	base, head := strings.Repeat("1", 40), strings.Repeat("2", 40)
	valid := string(data)
	for _, tc := range []struct{ code, content string }{
		{"json_invalid", "private-diagnostic: token=secret"},
		{"json_invalid", valid + "{}"},
		{"encoding_invalid", string([]byte{255})},
		{"findings_too_large", strings.Repeat("x", review.MaxFindingsBytes+1)},
		{"shape_invalid", strings.Replace(valid, `"schemaVersion":`, `"private-diagnostic":null,"schemaVersion":`, 1)},
		{"shape_invalid", strings.Replace(valid, `"schemaVersion":`, `"schemaVersion":"private-diagnostic","schemaVersion":`, 1)},
		{"shape_invalid", strings.Replace(valid, `"findings":`, `"Findings":`, 1)},
		{"types_invalid", strings.Replace(valid, `"schemaVersion": "1.0"`, `"schemaVersion": 1`, 1)},
		{"version_invalid", strings.Replace(valid, `"1.0"`, `"private-diagnostic"`, 1)},
		{"refs_mismatch", strings.Replace(valid, base, "private-diagnostic", 1)},
		{"text_invalid", strings.Replace(valid, "Check the nil input", " ", 1)},
		{"severity_invalid", strings.Replace(valid, `"HIGH"`, `"private-diagnostic"`, 1)},
		{"confidence_invalid", strings.Replace(valid, "0.95", "2", 1)},
		{"location_invalid", strings.Replace(valid, "src/review.go", "../private-diagnostic", 1)},
		{"location_invalid", strings.Replace(valid, `"endLine": 14`, `"endLine": 1`, 1)},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, err := review.ParseFindings([]byte(tc.content), base, head)
			if !errors.Is(err, review.ErrInvalidFindings) || review.OutputDiagnostic(err) != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			if strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("rejected contents leaked")
			}
		})
	}
	if review.ValidOutputDiagnostic("private-diagnostic") || review.OutputDiagnostic(review.NewOutputError("private-diagnostic")) != "findings_invalid" {
		t.Fatal("unknown code escaped")
	}
}
