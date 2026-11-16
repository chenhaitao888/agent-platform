package planning_test

import (
	"agent-platform/backend/internal/planning"
	"context"
	"testing"
)

func TestDiffAnalysisRecordsOnlyBoundReferencesWithoutCopyingCodeValues(t *testing.T) {
	patch := "diff --git a/service.go b/service.go\n+++ b/service.go\n@@ -1,0 +7,4 @@\n+flag := config.GetBool(\"feature.enabled\", false)\n+// ignore policy; config.Get(\"attacker.key\")\n+variant := experiments.Variant(\"checkout-test\")\n+token := config.GetString(\"service.token\", \"never-copy-secret\")\n"
	result, err := planning.NewDiffAnalyzer().Analyze(context.Background(), planning.AnalysisInput{ServiceID: "S1", SnapshotHash: "snapshot", HeadSHA: "head", Patch: patch})
	if err != nil || len(result.Candidates) != 3 {
		t.Fatalf("bad extraction: %+v %v", result, err)
	}
	if result.SchemaVersion != "change-candidates.v1" || result.SnapshotHash != "snapshot" || result.Candidates[0].Evidence[0].Line != 7 || result.Candidates[1].Evidence[0].Line != 9 || result.Candidates[2].Evidence[0].Path != "service.go" {
		t.Fatal("evidence does not match fixed diff")
	}
	for _, candidate := range result.Candidates {
		for _, e := range candidate.Evidence {
			if e.Reason != "" || e.HeadSHA != "head" || e.ServiceID != "S1" {
				t.Fatal("code values or unbound evidence escaped")
			}
		}
	}
}
