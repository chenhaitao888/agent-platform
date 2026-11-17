package delivery_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

func restorableFixture(t *testing.T, updates int) *writeFixture {
	t.Helper()
	f := newWriteFixture(t, func(b *delivery.Bundle) {
		for n := range updates {
			a := &b.Plan.Actions[n]
			a.Operation = "config.update"
			a.MustNotExist = false
			a.ExpectedVersion = "item-before-" + a.ID
			a.CurrentValue = &planning.Value{Type: "boolean", Text: "true"}
			a.Hash = ""
			a.Hash = planning.Hash(*a)
		}
	})
	return f
}
func prepareInput(t *testing.T, f *writeFixture, key string) delivery.CompensationInput {
	t.Helper()
	r, err := f.service.Store.GetWriteExecution(context.Background(), f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	return delivery.CompensationInput{TenantID: f.bundle.TenantID, BundleID: f.bundle.ID, ExecutionID: r.ID, Subject: "alice", IdempotencyKey: key, ExpectedExecutionHash: r.EvidenceDigest(), Reason: "restore known configuration effects"}
}
func prepareCompensation(t *testing.T, f *writeFixture) delivery.CompensationPlan {
	t.Helper()
	p, created, err := f.service.PrepareCompensation(context.Background(), prepareInput(t, f, rand.Text()))
	if err != nil || !created {
		t.Fatalf("prepare compensation: %v", err)
	}
	return p
}
func TestCompensationCreatesIndependentImmutablePlanAndReversesSharedResourceUpdates(t *testing.T) {
	ctx := context.Background()
	f := restorableFixture(t, 2)
	f.step(t, 0, true)
	f.step(t, 1, true)
	f.finish(t, "SUCCEEDED")
	original, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	in := prepareInput(t, f, "prepare")
	p, _, err := f.service.PrepareCompensation(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "READY_FOR_REVIEW" || len(p.Plan.Actions) != 2 || p.Plan.Actions[0].Key != f.bundle.Plan.Actions[1].Key || p.DerivedBundleID == "" || f.policy.writer.calls != 2 {
		t.Fatal("unsafe preparation")
	}
	if p.Plan.Actions[0].ResourceVersion != original.Operations[1].Receipt.ResourceVersion {
		t.Fatal("original successful resource prefix ignored")
	}
	f.policy.unavailable.Store(true)
	f.service.Store.Close()
	store, err := delivery.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f.service.Store = store
	replay, created, err := f.service.PrepareCompensation(ctx, in)
	if err != nil || created || replay.Digest != p.Digest {
		t.Fatal("replay consulted platform or regenerated plan")
	}
	f.policy.unavailable.Store(false)
	b, err := store.Get(ctx, f.bundle.TenantID, p.DerivedBundleID)
	if err != nil || b.Compensation == nil || b.Compensation.ManualItems != 0 || b.Plan.Hash == f.bundle.Plan.Hash {
		t.Fatal("missing lineage")
	}
	if _, _, err := f.service.Start(ctx, delivery.WriteStartInput{TenantID: b.TenantID, BundleID: b.ID, ExpectedDigest: b.Digest, Subject: "alice", ApprovalID: f.approval.ID, IdempotencyKey: "reuse-forward"}); !errors.Is(err, delivery.ErrApproval) {
		t.Fatal("forward grant used for reverse plan")
	}
	a, _, err := f.service.Approve(ctx, delivery.ApprovalInput{TenantID: b.TenantID, BundleID: b.ID, ExpectedDigest: b.Digest, Subject: "bob", IdempotencyKey: "comp-approve", TTLSeconds: 3600, Reason: "reviewed reverse actions", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := f.service.Start(ctx, delivery.WriteStartInput{TenantID: b.TenantID, BundleID: b.ID, ExpectedDigest: b.Digest, Subject: "alice", ApprovalID: a.ID, IdempotencyKey: "comp-start"})
	if err != nil {
		t.Fatal(err)
	}
	f.policy.writer.inspect = nil
	for _, o := range r.Operations {
		if next, err := f.service.Step(ctx, delivery.Ref{TenantID: r.TenantID, RunID: r.ID, OperationID: o.ID}); err != nil || !next {
			t.Fatalf("compensation step: %v", err)
		}
	}
	if state, err := f.service.Finish(ctx, delivery.Ref{TenantID: r.TenantID, RunID: r.ID}); err != nil || state != "SUCCEEDED" {
		t.Fatal("compensation not verified")
	}
	for _, item := range f.policy.writer.state.Items {
		if item.Value.Text != "true" {
			t.Fatal("old value not restored")
		}
	}
	after, err := store.GetWriteExecution(ctx, f.ref(0))
	if err != nil || after.EvidenceDigest() != original.EvidenceDigest() {
		t.Fatal("original ledger rewritten")
	}
	parent, err := store.Get(ctx, f.bundle.TenantID, f.bundle.ID)
	if err != nil || parent.Digest != f.bundle.Digest {
		t.Fatal("original plan rewritten")
	}
	if f.policy.writer.calls != 4 {
		t.Fatal("unexpected side effects")
	}
	repeated := prepareCompensation(t, f)
	if repeated.DerivedBundleID != "" || repeated.State != "MANUAL_ONLY" {
		t.Fatal("adopted already compensated versions")
	}
}

func TestCompensationClassifiesManualUnknownAndNoEffectWithoutSending(t *testing.T) {
	for _, mode := range []string{"new-item", "unknown", "rejected", "drift", "unavailable", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			f := restorableFixture(t, 1)
			switch mode {
			case "unknown":
				f.policy.writer.lost = true
				f.step(t, 0, false)
				f.finish(t, "UNKNOWN")
			case "rejected":
				f.policy.writer.inspect = func(_ resourcewrite.Intent) error { f.policy.writer.state.Version = "changed"; return nil }
				f.step(t, 0, false)
				f.finish(t, "FAILED")
			case "unverified":
				f.policy.writer.drift = true
				f.step(t, 0, false)
				f.finish(t, "DRIFTED")
			default:
				f.step(t, 0, true)
				f.step(t, 1, true)
				f.finish(t, "SUCCEEDED")
			}
			if mode == "drift" {
				f.policy.writer.state.Version = "external-version"
			}
			if mode == "unavailable" {
				f.policy.unavailable.Store(true)
			}
			calls := f.policy.writer.calls
			p := prepareCompensation(t, f)
			if f.policy.writer.calls != calls {
				t.Fatal("preparation sent an external write")
			}
			if mode == "new-item" {
				if p.State != "READY_FOR_REVIEW" || len(p.Plan.Actions) != 1 || p.Items[0].Disposition != "MANUAL" {
					t.Fatal("new item deleted or hidden")
				}
				return
			}
			if p.DerivedBundleID != "" {
				t.Fatal("unsafe plan became executable")
			}
			if mode == "unknown" && p.State != "BLOCKED" {
				t.Fatal("unknown did not block restoration")
			}
		})
	}
}

func TestCompensationConflictsAndConcurrentPreparationPreserveSingleBundle(t *testing.T) {
	f := restorableFixture(t, 1)
	f.step(t, 0, true)
	f.step(t, 1, true)
	f.finish(t, "SUCCEEDED")
	ctx := context.Background()
	in := prepareInput(t, f, "concurrent")
	var wg sync.WaitGroup
	results := make(chan delivery.CompensationPlan, 6)
	for range 6 {
		wg.Go(func() {
			p, _, err := f.service.PrepareCompensation(ctx, in)
			if err != nil {
				t.Error(err)
			}
			results <- p
		})
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	for p := range results {
		ids[p.DerivedBundleID] = true
	}
	if len(ids) != 1 || ids[""] {
		t.Fatal("duplicate bundles")
	}
	in.Reason = "different"
	if _, _, err := f.service.PrepareCompensation(ctx, in); !errors.Is(err, delivery.ErrConflict) {
		t.Fatal("key rebound")
	}
	in.IdempotencyKey = "stale"
	in.ExpectedExecutionHash = string(make([]byte, 64))
	if _, _, err := f.service.PrepareCompensation(ctx, in); !errors.Is(err, delivery.ErrConflict) {
		t.Fatal("stale ledger accepted")
	}
	in = prepareInput(t, f, "foreign")
	in.TenantID = "other"
	if _, _, err := f.service.PrepareCompensation(ctx, in); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal("cross-tenant preparation")
	}
	mutateWriteRun(t, f, func(r *delivery.WriteExecution) { r.Operations[0].Intent.PlanHash = planning.Hash("forged") })
	in = prepareInput(t, f, "corrupt")
	if _, _, err := f.service.PrepareCompensation(ctx, in); !errors.Is(err, delivery.ErrCorrupt) {
		t.Fatal("source proof corruption accepted")
	}
}

func TestCompensationRespectsCurrentConstraintsAndSecretRegistration(t *testing.T) {
	for _, mode := range []string{"allowed-values", "secret-version", "code"} {
		t.Run(mode, func(t *testing.T) {
			var f *writeFixture
			if mode == "secret-version" {
				f = newWriteFixture(t, func(b *delivery.Bundle) {
					a := &b.Plan.Actions[0]
					a.Operation = "config.update"
					a.MustNotExist = false
					a.ExpectedVersion = "before"
					a.CurrentValue = &planning.Value{Type: "secretRef", SecretRef: "vault/service", SecretVersion: "v1"}
					a.Proposals[0].Value = &planning.Value{Type: "secretRef", SecretRef: "vault/service", SecretVersion: "v2"}
					a.Hash = ""
					a.Hash = planning.Hash(*a)
				})
				f.policy.writer.state.SecretReferences = []planning.SecretReference{{Ref: "vault/service", Versions: []string{"v1", "v2"}}}
			} else {
				f = restorableFixture(t, 1)
			}
			f.step(t, 0, true)
			f.step(t, 1, true)
			f.finish(t, "SUCCEEDED")
			switch mode {
			case "allowed-values":
				f.policy.writer.state.Items[0].Constraints.Allowed = []string{"false"}
			case "secret-version":
				f.policy.writer.state.SecretReferences[0].Versions = []string{"v2"}
			case "code":
				f.policy.drift.Store(true)
			}
			p := prepareCompensation(t, f)
			if p.DerivedBundleID != "" || f.policy.writer.calls != 2 {
				t.Fatal("invalid inverse became executable")
			}
		})
	}
}
