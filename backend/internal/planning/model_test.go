package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func modelInput() AnalysisInput {
	return AnalysisInput{ServiceID: "S1", SnapshotHash: strings.Repeat("a", 64), BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("c", 40), Environment: "test", Goal: "Use the checkout limit", Patch: "diff --git a/app.go b/app.go\n+++ b/app.go\n@@ -1,1 +1,3 @@\n old()\n+limit := settings.Integer(limitKey)\n+config.GetBool(\"literal.flag\", false)\n", Resources: []ResourceContext{}}
}
func outputFor(in AnalysisInput) modelOutput {
	return modelOutput{SchemaVersion: "model-candidates.v1", InputHash: Hash(in), SnapshotHash: in.SnapshotHash, ServiceID: in.ServiceID, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, Candidates: []modelCandidate{{Kind: "config", Key: "checkout.limit", Reason: "The wrapper reads limitKey.", Evidence: []modelEvidence{{Path: "app.go", Line: 2}}}}, Questions: []AnalysisQuestion{}}
}
func encoded(value any) []byte { data, _ := json.Marshal(value); return data }

func TestModelCandidateOutputRejectsUnboundEvidenceAndWriteFields(t *testing.T) {
	in := modelInput()
	for _, scenario := range []string{"valid", "valid-experiment", "head", "base", "service", "snapshot", "input", "line", "context-line", "path", "duplicate", "no-evidence", "blank-reason", "missing-candidates", "missing-questions", "wrong-kind", "desired", "binding", "unknown", "null", "duplicate-field", "case-alias", "oversize", "trailing"} {
		t.Run(scenario, func(t *testing.T) {
			value := outputFor(in)
			switch scenario {
			case "valid-experiment":
				value.Candidates[0].Kind = "experiment"
				value.Candidates[0].Key = "checkout-experiment"
			case "head":
				value.HeadSHA = in.BaseSHA
			case "base":
				value.BaseSHA = in.HeadSHA
			case "service":
				value.ServiceID = "S2"
			case "snapshot":
				value.SnapshotHash = strings.Repeat("d", 64)
			case "input":
				value.InputHash = strings.Repeat("d", 64)
			case "line":
				value.Candidates[0].Evidence[0].Line = 99
			case "context-line":
				value.Candidates[0].Evidence[0].Line = 1
			case "path":
				value.Candidates[0].Evidence[0].Path = "../app.go"
			case "duplicate":
				value.Candidates = append(value.Candidates, value.Candidates[0])
			case "no-evidence":
				value.Candidates[0].Evidence = nil
			case "blank-reason":
				value.Candidates[0].Reason = " "
			case "missing-candidates":
				value.Candidates = nil
			case "missing-questions":
				value.Questions = nil
			case "wrong-kind":
				value.Candidates[0].Kind = "activate"
			}
			data := string(encoded(value))
			switch scenario {
			case "desired":
				data = strings.Replace(data, `"kind":"config"`, `"desired":{"type":"integer","text":"99"},"kind":"config"`, 1)
			case "binding":
				data = strings.Replace(data, `"kind":"config"`, `"connectorRef":"evil","kind":"config"`, 1)
			case "unknown":
				data = strings.Replace(data, `"kind":"config"`, `"approved":true,"kind":"config"`, 1)
			case "null":
				data = strings.Replace(data, `"reason":"The wrapper reads limitKey."`, `"reason":null`, 1)
			case "duplicate-field":
				data = strings.Replace(data, `"serviceId":"S1"`, `"serviceId":"S1","serviceId":"S1"`, 1)
			case "case-alias":
				data = strings.Replace(data, `"headSha"`, `"HeadSha"`, 1)
			case "oversize":
				data += strings.Repeat(" ", MaxModelAnalysisBytes)
			case "trailing":
				data += "{}"
			}
			result, err := ParseModelAnalysis([]byte(data), in)
			if strings.HasPrefix(scenario, "valid") {
				if err != nil || result.Trace.InputHash != Hash(in) || result.Candidates[0].Evidence[0].HeadSHA != in.HeadSHA {
					t.Fatalf("valid: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid candidate accepted")
			}
		})
	}
}

type modelStub struct {
	call func(AnalysisInput) ([]byte, error)
}

func (s modelStub) RunCandidates(_ context.Context, in AnalysisInput) ([]byte, error) {
	return s.call(in)
}
func (modelStub) CandidateIdentity() ModelIdentity {
	return ModelIdentity{Model: "fixture-model", RuntimeHash: strings.Repeat("d", 64)}
}

type contextReader struct {
	reads int
	fail  bool
}

func (*contextReader) Describe() Descriptor {
	return Descriptor{Kind: "config", ContractVersion: "config-read.v1", ImplementationVersion: "fixture.v1", ConnectionVersion: "1"}
}
func (r *contextReader) ReadConfig(_ context.Context, target Target) (ConfigState, error) {
	r.reads++
	if r.fail {
		return ConfigState{}, ErrUnavailable
	}
	return ConfigState{Target: target, Version: "1", SaveMode: "draft", ConditionalUpdates: true, Items: []ConfigItem{{Key: "checkout.limit", Version: "1", Value: Value{Type: "integer", Text: "98765"}}, {Key: "service.secret", Version: "1", Sensitive: true, Value: Value{Type: "secretRef", SecretRef: "never-send-ref", SecretVersion: "private-version"}}}}, nil
}

func TestModelPlanRetainsRulesAndUsesRedactedSharedObservations(t *testing.T) {
	in := modelInput()
	reader := &contextReader{}
	calls := 0
	analyzer, _ := NewModelAnalyzer(modelStub{call: func(input AnalysisInput) ([]byte, error) {
		calls++
		for _, secret := range []string{"98765", "never-send-ref", "private-version", "connector-ref", "resource-ref"} {
			if strings.Contains(string(encoded(input)), secret) {
				t.Fatal("platform data leaked to model")
			}
		}
		if len(input.Resources) != 2 || input.Resources[0].Status != "AVAILABLE" || len(input.Resources[0].Items) != 2 {
			t.Fatal("missing resource metadata")
		}
		return encoded(outputFor(input)), nil
	}})
	planner, _ := NewPlanner(map[string]ConfigReader{"connector-ref": reader}, nil, analyzer)
	service := ServiceInput{Source: Source{ServiceID: in.ServiceID, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, Review: ReviewEvidence{State: "SUCCEEDED"}}, Patch: in.Patch, Bindings: EnvironmentBinding{Config: &ResourceBinding{ConnectorRef: "connector-ref", ResourceRef: "resource-ref"}}}
	build := Input{SnapshotHash: in.SnapshotHash, TenantID: "tenant", Environment: in.Environment, Goal: in.Goal, Services: []ServiceInput{service, service}}
	plan := planner.Build(context.Background(), build)
	if calls != 2 || reader.reads != 1 || len(plan.Observations) != 1 || len(plan.Actions) != 2 || plan.State != "BLOCKED" {
		t.Fatalf("bad merge %+v, reads=%d calls=%d", plan, reader.reads, calls)
	}
	for _, source := range plan.Services {
		if len(source.Candidates) != 2 || source.Analysis == nil || source.Analysis.Identity.Model != "fixture-model" || source.Candidates[1].Origin != "literal" {
			t.Fatal("missing model/rule evidence")
		}
	}
	for _, action := range plan.Actions {
		for _, proposal := range action.Proposals {
			if proposal.Value != nil {
				t.Fatal("model supplied a desired value")
			}
		}
	}
}

func TestModelFailureQuestionsAndEmptyScanNeverPassCoverage(t *testing.T) {
	for _, mode := range []string{"failure", "empty", "question", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			in := modelInput()
			reader := &contextReader{fail: mode == "read-failure"}
			analyzer, _ := NewModelAnalyzer(modelStub{call: func(input AnalysisInput) ([]byte, error) {
				if mode == "failure" {
					return nil, errors.New("private provider failure")
				}
				result := outputFor(input)
				result.Candidates = []modelCandidate{}
				if mode == "question" {
					result.Questions = []AnalysisQuestion{{Kind: "config", Reason: "Dynamic key depends on runtime input"}}
				}
				return encoded(result), nil
			}})
			planner, _ := NewPlanner(map[string]ConfigReader{"ref": reader}, nil, analyzer)
			plan := planner.Build(context.Background(), Input{SnapshotHash: in.SnapshotHash, Environment: in.Environment, Services: []ServiceInput{{Source: Source{ServiceID: in.ServiceID, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, Review: ReviewEvidence{State: "SUCCEEDED"}}, Patch: strings.ReplaceAll(in.Patch, "config.GetBool", "other.Flag"), Bindings: EnvironmentBinding{Config: &ResourceBinding{ConnectorRef: "ref", ResourceRef: "resource"}}}}})
			if plan.State != "BLOCKED" {
				t.Fatal("incomplete analysis passed")
			}
			expected := "coverage_confirmation_required"
			switch mode {
			case "failure":
				expected = "analysis_failed"
			case "question":
				expected = "analysis_question"
			case "read-failure":
				expected = "platform_read_failed"
			}
			found := false
			for _, issue := range plan.Issues {
				if issue.Code == expected {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", expected, plan.Issues)
			}
			if mode == "failure" && (plan.Services[0].Analysis != nil || plan.Services[0].ConfigCoverage != "ANALYSIS_FAILED") {
				t.Fatal("failure fell back silently")
			}
		})
	}
}
