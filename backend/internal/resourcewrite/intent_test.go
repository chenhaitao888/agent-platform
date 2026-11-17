package resourcewrite_test

import (
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

func testPlan() planning.Plan {
	plan := planning.Plan{SchemaVersion: "change-plan.v1", TenantID: "tenant", Environment: "test", State: "READY_FOR_REVIEW", Actions: []planning.Action{}}
	for _, key := range []string{"first", "second"} {
		a := planning.Action{ID: key, Kind: "config", Key: key, Operation: "config.create", Effect: "draft", Target: planning.Target{TenantID: "tenant", Environment: "test", ConnectorRef: "config", ResourceRef: "namespace"}, ResourceVersion: "v1", MustNotExist: true, Descriptor: planning.Descriptor{Kind: "config", ContractVersion: "config-read.v1", ImplementationVersion: "reference-http.v1", ConnectionVersion: "1"}, Proposals: []planning.Proposal{{ServiceID: "S1", Value: &planning.Value{Type: "integer", Text: "20"}}}}
		a.Hash = planning.Hash(a)
		plan.Actions = append(plan.Actions, a)
	}
	plan.Hash = planning.Hash(plan)
	return plan
}
func descriptor() planning.Descriptor {
	return planning.Descriptor{Kind: "config", ContractVersion: "config-write.v1", ImplementationVersion: "reference-http.v1", ConnectionVersion: "1"}
}
func applied(i resourcewrite.Intent) resourcewrite.Receipt {
	return resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", ID: "receipt-1", OperationID: i.OperationID, IntentHash: i.Hash, Kind: i.Kind, Target: i.Target, Key: i.Key, Status: "APPLIED", BeforeResourceVersion: i.ResourceVersion, ResourceVersion: "v2", ItemVersion: "item-v2", ValueHash: i.ValueDigest(), Effect: i.Effect, RecordedAt: time.Now().UTC()}
}
func completed(t *testing.T, i resourcewrite.Intent) resourcewrite.Completed {
	t.Helper()
	r := applied(i)
	v, err := resourcewrite.VerifyConfig(i, r, planning.ConfigState{Target: i.Target, Version: r.ResourceVersion, SaveMode: i.Effect, ConditionalUpdates: true, Items: []planning.ConfigItem{{Key: i.Key, Version: r.ItemVersion, Value: *i.Value}}})
	if err != nil || !v.ValidFor(i, r) {
		t.Fatalf("verification: %+v %v", v, err)
	}
	return resourcewrite.Completed{Intent: i, Receipt: r, Verification: v}
}

func TestIntentUsesOnlyVerifiedPrefixToAdvanceSharedResourceVersion(t *testing.T) {
	plan := testPlan()
	expiry := time.Now().UTC().Add(time.Hour)
	first, err := resourcewrite.Prepare(plan, 0, "op-first", descriptor(), expiry, nil)
	if err != nil {
		t.Fatal(err)
	}
	prefix := []resourcewrite.Completed{completed(t, first)}
	second, err := resourcewrite.Prepare(plan, 1, "op-second", descriptor(), expiry, prefix)
	if err != nil || second.ResourceVersion != "v2" || second.OriginalResourceVersion != "v1" || second.ActionHash != plan.Actions[1].Hash || second.PlanHash != plan.Hash {
		t.Fatalf("unsafe successor: %+v %v", second, err)
	}
	for _, mode := range []string{"no-proof", "unverified", "wrong-plan", "wrong-action", "wrong-target", "wrong-receipt", "duplicate-operation", "receipt-only", "baseline-drift"} {
		t.Run(mode, func(t *testing.T) {
			p := planning.Clone(prefix)
			current := planning.Clone(plan)
			op := "op-second"
			switch mode {
			case "no-proof":
				p = nil
			case "unverified":
				p[0].Verification.State = "DRIFTED"
			case "wrong-plan":
				p[0].Intent.PlanHash = strings.Repeat("b", 64)
			case "wrong-action":
				p[0].Intent.ActionID = "second"
			case "wrong-target":
				p[0].Receipt.Target.Environment = "prod"
			case "wrong-receipt":
				p[0].Receipt.ValueHash = strings.Repeat("e", 64)
			case "duplicate-operation":
				op = "op-first"
			case "receipt-only":
				p[0].Verification = resourcewrite.Verification{}
			case "baseline-drift":
				current.Actions[1].ResourceVersion = "external-v3"
				current.Actions[1].Hash = ""
				current.Actions[1].Hash = planning.Hash(current.Actions[1])
				current.Hash = ""
				current.Hash = planning.Hash(current)
			}
			if _, err := resourcewrite.Prepare(current, 1, op, descriptor(), expiry, p); err == nil {
				t.Fatal("untrusted version advancement accepted")
			}
		})
	}
	// Inputs remain immutable even if a caller modifies its source plan.
	plan.Actions[0].Proposals[0].Value.Text = "999"
	if first.Value.Text != "20" || !first.Valid() {
		t.Fatal("intent aliases mutable plan")
	}
}

func TestIntentRejectsAlteredAndConflictingPlanInputs(t *testing.T) {
	for _, mode := range []string{"blocked", "hash", "action-hash", "conflict", "missing-value", "activation", "bad-target", "writer-version", "create-version"} {
		t.Run(mode, func(t *testing.T) {
			plan := testPlan()
			writer := descriptor()
			switch mode {
			case "blocked":
				plan.State = "BLOCKED"
			case "hash":
				plan.Hash = "bad"
			case "action-hash":
				plan.Actions[0].Hash = "bad"
			case "conflict":
				plan.Actions[0].Proposals = append(plan.Actions[0].Proposals, planning.Proposal{ServiceID: "S2", Value: &planning.Value{Type: "integer", Text: "25"}})
			case "missing-value":
				plan.Actions[0].Proposals[0].Value = nil
			case "activation":
				plan.Actions[0].Operation = "experiment.activate"
			case "bad-target":
				plan.Actions[0].Target.TenantID = "other"
			case "writer-version":
				writer.ConnectionVersion = "2"
			case "create-version":
				plan.Actions[0].ExpectedVersion = "item-v1"
			}
			if mode != "action-hash" {
				plan.Actions[0].Hash = ""
				plan.Actions[0].Hash = planning.Hash(plan.Actions[0])
			}
			if mode != "hash" {
				plan.Hash = ""
				plan.Hash = planning.Hash(plan)
			}
			if _, err := resourcewrite.Prepare(plan, 0, "operation", writer, time.Now().Add(time.Hour), nil); err == nil {
				t.Fatal("unsafe intent accepted")
			}
		})
	}
}

func TestVerificationPreservesAppliedFactButDetectsSubsequentDrift(t *testing.T) {
	i, _ := resourcewrite.Prepare(testPlan(), 0, "operation", descriptor(), time.Now().Add(time.Hour), nil)
	r := applied(i)
	for _, mode := range []string{"version", "value", "effect", "missing"} {
		t.Run(mode, func(t *testing.T) {
			state := planning.ConfigState{Target: i.Target, Version: r.ResourceVersion, SaveMode: i.Effect, Items: []planning.ConfigItem{{Key: i.Key, Version: r.ItemVersion, Value: *i.Value}}}
			switch mode {
			case "version":
				state.Version = "external-v3"
			case "value":
				state.Items[0].Value.Text = "30"
			case "effect":
				state.SaveMode = "immediate"
			case "missing":
				state.Items = []planning.ConfigItem{}
			}
			v, err := resourcewrite.VerifyConfig(i, r, state)
			if err != nil || v.State != "DRIFTED" || !r.ValidFor(i) || r.Status != "APPLIED" {
				t.Fatalf("lost applied fact: %+v %v", v, err)
			}
		})
	}
}
