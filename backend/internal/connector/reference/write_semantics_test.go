package reference_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

func resealWritePlan(p planning.Plan) planning.Plan {
	for n := range p.Actions {
		p.Actions[n].Hash = ""
		p.Actions[n].Hash = planning.Hash(p.Actions[n])
	}
	p.Hash = ""
	p.Hash = planning.Hash(p)
	return p
}
func TestConfigNoChangeAndSecretReferencesHavePreciseEffects(t *testing.T) {
	for _, mode := range []string{"no-change", "secret", "unregistered-secret", "out-of-range", "type-change", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			data := initialWriteDatabase()
			plan := writePlan()
			plan.Actions = plan.Actions[:1]
			switch mode {
			case "no-change":
				plan.Actions[0].Operation = "config.no_change"
				plan.Actions[0].Proposals[0].Value.Text = "10"
			case "secret", "unregistered-secret":
				ref := "vault/service"
				if mode == "unregistered-secret" {
					ref = "unregistered"
				}
				data.Config.Items = []planning.ConfigItem{}
				plan.Actions[0].Operation = "config.create"
				plan.Actions[0].MustNotExist = true
				plan.Actions[0].ExpectedVersion = ""
				plan.Actions[0].CurrentValue = nil
				plan.Actions[0].Proposals[0].Value = &planning.Value{Type: "secretRef", SecretRef: ref, SecretVersion: "v1"}
			case "out-of-range":
				maximum := 15.0
				data.Config.Items[0].Constraints.Max = &maximum
			case "type-change":
				plan.Actions[0].Proposals[0].Value = &planning.Value{Type: "string", Text: "20"}
			case "unsupported":
				data.Config.ConditionalUpdates = false
			}
			plan = resealWritePlan(plan)
			backend := &memoryWriteBackend{data: data}
			platform := newWritePlatform(t, backend)
			writer := writerFor(t, platform, "config")
			intent := prepareWrite(t, plan, 0, writer, nil)
			receipt, err := writer.Apply(context.Background(), intent)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "no-change" || mode == "secret" {
				v, err := writer.Verify(context.Background(), intent, receipt)
				if receipt.Status != "APPLIED" || err != nil || !v.ValidFor(intent, receipt) {
					t.Fatalf("missing verified result: %+v %v", receipt, err)
				}
				expected := 1
				if mode == "no-change" {
					expected = 0
					if receipt.ResourceVersion != "resource-1" || receipt.ItemVersion != "item-1" {
						t.Fatal("no-change advanced versions")
					}
				}
				if snapshotWriteBackend(t, backend).Effects != expected {
					t.Fatal("wrong effect count")
				}
			} else if receipt.Status != "REJECTED" || snapshotWriteBackend(t, backend).Effects != 0 {
				t.Fatal("unsafe value or capability accepted")
			}
		})
	}
}

func TestExperimentWritesRequireAnInactiveDraftAndVerifyLifecycle(t *testing.T) {
	for _, mode := range []string{"update-draft", "active", "zero-traffic-is-not-draft", "no-cas", "allocation", "exclusion"} {
		t.Run(mode, func(t *testing.T) {
			plan := writePlan()
			plan.Actions = plan.Actions[2:]
			a := &plan.Actions[0]
			data := initialWriteDatabase()
			original := planning.Clone(*a.Proposals[0].Experiment)
			data.Experiments.Items = []planning.ExperimentItem{{Key: a.Key, Version: "item-1", State: "DRAFT", Spec: original}}
			a.Operation = "experiment.update_draft"
			a.MustNotExist = false
			a.ExpectedVersion = "item-1"
			a.CurrentExperiment = &original
			a.Proposals[0].Experiment.Variants[0].Weight = 25
			a.Proposals[0].Experiment.Variants[1].Weight = 75
			switch mode {
			case "active":
				data.Experiments.Items[0].State = "ACTIVE"
			case "zero-traffic-is-not-draft":
				data.Experiments.DraftNoTraffic = false
			case "no-cas":
				data.Experiments.ConditionalUpdates = false
			case "allocation":
				data.Experiments.AllocationModes = []string{"unsupported"}
			case "exclusion":
				a.Proposals[0].Experiment.ExclusionGroup = "checkout-group"
				data.Experiments.SupportsExclusion = false
			}
			plan = resealWritePlan(plan)
			backend := &memoryWriteBackend{data: data}
			platform := newWritePlatform(t, backend)
			writer := writerFor(t, platform, "experiment")
			intent := prepareWrite(t, plan, 0, writer, nil)
			receipt, err := writer.Apply(context.Background(), intent)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "update-draft" {
				if receipt.Status != "REJECTED" || snapshotWriteBackend(t, backend).Effects != 0 {
					t.Fatal("unsafe experiment change accepted")
				}
				return
			}
			verification, err := writer.Verify(context.Background(), intent, receipt)
			if err != nil || !verification.ValidFor(intent, receipt) {
				t.Fatal("draft update not verified")
			}
			_ = backend.transaction(context.Background(), func(data *writeDatabase) error { data.Experiments.Items[0].State = "ACTIVE"; return nil })
			verification, err = writer.Verify(context.Background(), intent, receipt)
			if err != nil || verification.State != "DRIFTED" || receipt.Status != "APPLIED" {
				t.Fatal("activation drift hidden")
			}
		})
	}
}

func TestLookupNotFoundWhileOriginalRequestIsInFlightNeverTriggersRetry(t *testing.T) {
	backend := &memoryWriteBackend{data: initialWriteDatabase()}
	platform := newWritePlatform(t, backend)
	platform.entered = make(chan struct{}, 1)
	platform.release = make(chan struct{})
	writer := writerFor(t, platform, "config")
	intent := prepareWrite(t, writePlan(), 0, writer, nil)
	result := make(chan resourcewrite.Receipt, 1)
	fail := make(chan error, 1)
	go func() { r, err := writer.Apply(context.Background(), intent); result <- r; fail <- err }()
	select {
	case <-platform.entered:
	case <-time.After(2 * time.Second):
		close(platform.release)
		t.Fatal("provider did not receive request")
	}
	if _, err := writer.Lookup(context.Background(), intent); !errors.Is(err, resourcewrite.ErrUnknown) {
		close(platform.release)
		t.Fatal("absence treated as non-execution")
	}
	close(platform.release)
	r := <-result
	if err := <-fail; err != nil || r.Status != "APPLIED" {
		t.Fatal("original request did not finish")
	}
	found, err := writer.Lookup(context.Background(), intent)
	if err != nil || found.ID != r.ID || platform.calls.Load() != 3 || snapshotWriteBackend(t, backend).Effects != 1 {
		t.Fatal("lookup repeated or lost the original effect")
	}
}

func TestExpiredIntentDoesNotHideAnEarlierCommittedReceipt(t *testing.T) {
	backend := &memoryWriteBackend{data: initialWriteDatabase()}
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	intent := prepareWrite(t, writePlan(), 0, writer, nil)
	intent.ExpiresAt = time.Now().UTC().Add(300 * time.Millisecond)
	intent.Hash = intent.Digest()
	done := executeVerified(t, writer, intent)
	time.Sleep(time.Until(intent.ExpiresAt) + 10*time.Millisecond)
	found, err := writer.Lookup(context.Background(), intent)
	if err != nil || found.ID != done.Receipt.ID {
		t.Fatal("expiry erased committed fact")
	}
	if v, err := writer.Verify(context.Background(), intent, found); err != nil || !v.ValidFor(intent, found) {
		t.Fatal("expiry prevented read-only verification")
	}
	if snapshotWriteBackend(t, backend).Effects != 1 {
		t.Fatal("expiry triggered another effect")
	}
}
