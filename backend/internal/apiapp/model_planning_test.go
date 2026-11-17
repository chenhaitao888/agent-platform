package apiapp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
)

const semanticFeature = "const limitKey = \"checkout.limit\"\nlimit := settings.Integer(limitKey)\nflag := config.GetBool(\"literal.flag\", false)\n"

func TestModelPlanningDeploymentRequiresExplicitCompatibleRuntime(t *testing.T) {
	for _, scenario := range []string{"valid", "unknown", "no-review", "no-planning", "old-worker"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, scenario != "no-review")
			platform := installPlanningFixture(t, fixture)
			t.Setenv("AGENT_PLATFORM_PLANNING_ANALYZER", "model")
			switch scenario {
			case "unknown":
				t.Setenv("AGENT_PLATFORM_PLANNING_ANALYZER", "arbitrary")
			case "no-planning":
				t.Setenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE", "")
			case "old-worker":
				if os.WriteFile(filepath.Join(fixture.directory, "old-worker"), []byte("old"), 0o600) != nil {
					t.Fatal("marker")
				}
			}
			app, err := apiapp.FromEnvironment(context.Background())
			if app != nil {
				defer app.Close()
			}
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid deployment accepted")
			}
			if platform.reads.Load() != 0 || fixture.modelCalls.Load() != 0 {
				t.Fatal("startup performed analysis or resource read")
			}
		})
	}
}

func TestModelPlansBindSemanticCandidatesAndNeverRepeatInferenceOnReads(t *testing.T) {
	fixture := newDeploymentFixture(t, semanticFeature)
	fixture.configure(t, true)
	platform := installPlanningFixture(t, fixture, true)
	t.Setenv("AGENT_PLATFORM_PLANNING_ANALYZER", "model")
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Handler)
	defer server.Close()
	address := server.URL
	services := `[{"serviceId":"S1","sourceBranch":"feature"},{"serviceId":"S2","sourceBranch":"feature"}]`
	apiNetworkWrite(t, address, "POST", "/api/v1/changes", `{"idempotencyKey":"create","title":"Checkout dependency","environment":"test","services":`+services+`}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"freeze","expectedVersion":1,"services":`+services+`}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	body := `{"idempotencyKey":"plan","expectedVersion":4,"snapshotId":"snapshot-1"}`
	first := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", body, 201)
	var result change.PlanResult
	if json.Unmarshal(first, &result) != nil || result.Plan.State != "BLOCKED" || result.Plan.AnalyzerVersion != "model-and-literal.v1" || len(result.Plan.Actions) != 2 {
		t.Fatalf("model plan: %s", first)
	}
	for _, source := range result.Plan.Services {
		if source.Analysis == nil || source.Analysis.Identity.Model != "deployment-review-model" || len(source.Candidates) != 2 || source.Candidates[0].Key != "checkout.limit" || source.Candidates[0].Evidence[0].Line != 2 || source.Candidates[1].Origin != "literal" {
			t.Fatalf("missing evidence: %+v", source)
		}
	}
	if fixture.modelCalls.Load() != 4 || platform.reads.Load() != 2 {
		t.Fatalf("unexpected work: model %d platform %d", fixture.modelCalls.Load(), platform.reads.Load())
	}
	replay := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", body, 200)
	apiNetworkWrite(t, address, "GET", "/api/v1/changes/change-1/plans", "", 200)
	apiNetworkWrite(t, address, "GET", "/api/v1/changes/change-1/plans/plan-1", "", 200)
	if string(first) != string(replay) || fixture.modelCalls.Load() != 4 || platform.reads.Load() != 2 {
		t.Fatal("replay/read repeated work")
	}
	// Explicit desired values enter the deterministic planner, never the model prompt.
	requirements := []planning.Requirement{}
	for _, id := range []string{"S1", "S2"} {
		requirements = append(requirements, planning.Requirement{ServiceID: id, Config: []planning.ConfigRequirement{{Key: "checkout.limit", Desired: &planning.Value{Type: "integer", Text: "314159"}, Reason: "Business acceptance"}, {Key: "literal.flag", Desired: &planning.Value{Type: "boolean", Text: "false"}, Reason: "Keep disabled"}}, ExperimentNotInvolved: "Reviewed feature; no experiment changes"})
	}
	input := change.PlanInput{IdempotencyKey: "complete", ExpectedVersion: 6, SnapshotID: "snapshot-1", Requirements: requirements}
	data, _ := json.Marshal(input)
	ready := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", string(data), 201)
	if json.Unmarshal(ready, &result) != nil || result.Plan.State != "READY_FOR_REVIEW" || len(result.Plan.Actions) != 2 {
		t.Fatalf("ready plan: %s", ready)
	}
	input.IdempotencyKey = "invalid-output"
	input.ExpectedVersion = 8
	if os.WriteFile(filepath.Join(fixture.directory, "invalid-candidate"), []byte("invalid"), 0o600) != nil {
		t.Fatal("marker")
	}
	data, _ = json.Marshal(input)
	invalid := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", string(data), 201)
	if json.Unmarshal(invalid, &result) != nil || result.Plan.State != "BLOCKED" || len(result.Plan.Actions) != 0 {
		t.Fatalf("invalid output used: %s", invalid)
	}
	for _, source := range result.Plan.Services {
		if source.ConfigCoverage != "ANALYSIS_FAILED" {
			t.Fatal("silent literal fallback")
		}
	}
	captured := string(readLocalFile(t, filepath.Join(fixture.directory, "review-inputs.jsonl")))
	for _, secret := range []string{"314159", "planning-fixture-token", "api-model-token", "api-gitlab-token", "shared/settings", "secretRef"} {
		if strings.Contains(captured, secret) {
			t.Fatalf("model input contains %s", secret)
		}
	}
	// All recorded sources remain fixed even when the branch later moves.
	if os.WriteFile(filepath.Join(fixture.directory, "source-drift"), []byte("drift"), 0o600) != nil {
		t.Fatal("marker")
	}
	old := apiNetworkWrite(t, address, "GET", "/api/v1/changes/change-1/plans/plan-1", "", 200)
	var prior planning.Plan
	if json.Unmarshal(old, &prior) != nil || prior.Services[0].HeadSHA != fixture.head {
		t.Fatal("history changed")
	}
	if fixture.modelCalls.Load() != 8 || platform.reads.Load() != 6 {
		t.Fatalf("unexpected reanalysis %d/%d", fixture.modelCalls.Load(), platform.reads.Load())
	}
}

func TestModelPlanArchiveSurvivesAPIRestart(t *testing.T) {
	databaseFile := archiveTestFile(t)
	fixture := newDeploymentFixture(t, semanticFeature)
	fixture.configure(t, true)
	installPlanningFixture(t, fixture, true)
	configureArchiveFixture(t, databaseFile)
	t.Setenv("AGENT_PLATFORM_PLANNING_ANALYZER", "model")
	binary := buildAPI(t)
	process := startAPI(t, binary)
	address := process.address
	apiNetworkWrite(t, address, "POST", "/api/v1/changes", `{"idempotencyKey":"create","title":"Model archive","environment":"test","services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"freeze","expectedVersion":1,"services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	var result change.PlanResult
	data := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", `{"idempotencyKey":"plan","expectedVersion":4,"snapshotId":"snapshot-1","requirements":[{"serviceId":"S1","config":[{"key":"checkout.limit","desired":{"type":"integer","text":"20"},"reason":"Requirement"},{"key":"literal.flag","desired":{"type":"boolean","text":"false"},"reason":"Keep disabled"}],"experimentNotInvolved":"No experiment change"}]}`, 201)
	if json.Unmarshal(data, &result) != nil || result.Plan.State != "READY_FOR_REVIEW" {
		t.Fatalf("model plan: %s", data)
	}
	body, _ := json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedPlanHash": result.Plan.Hash})
	var bundle delivery.Bundle
	saved := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans/plan-1/archive", string(body), 201)
	if json.Unmarshal(saved, &bundle) != nil || bundle.Plan.Services[0].Analysis == nil {
		t.Fatalf("archive: %s", saved)
	}
	process.stop(t)
	process = startAPI(t, binary)
	restored := apiNetworkWrite(t, process.address, "GET", "/api/v1/deliveries/"+bundle.ID, "", 200)
	replay := apiNetworkWrite(t, process.address, "POST", "/api/v1/changes/change-1/plans/plan-1/archive", string(body), 200)
	if string(restored) != string(saved) || string(replay) != string(saved) || fixture.modelCalls.Load() != 2 {
		t.Fatal("model archive changed or reanalyzed after restart")
	}
}
