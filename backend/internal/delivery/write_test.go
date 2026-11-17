package delivery_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"github.com/jackc/pgx/v5"
)

// The control-plane tests use a deterministic provider port. The reference
// HTTPS provider's transactional storage/restarts are tested in connector/reference.
type ledgerWriter struct {
	mu                  sync.Mutex
	state               planning.ConfigState
	receipts            map[string]resourcewrite.Receipt
	calls, lookups      int
	lost, absent, drift bool
	entered, release    chan struct{}
	inspect             func(resourcewrite.Intent) error
	version             atomic.Bool
}

func (w *ledgerWriter) Describe() planning.Descriptor {
	d := planning.Descriptor{Kind: "config", ContractVersion: "config-write.v1", ImplementationVersion: "fixture.v1", ConnectionVersion: "1"}
	if w.version.Load() {
		d.ImplementationVersion = "fixture.v2"
	}
	return d
}
func (w *ledgerWriter) Apply(ctx context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	if w.inspect != nil {
		if err := w.inspect(i); err != nil {
			return resourcewrite.Receipt{}, err
		}
	}
	if w.entered != nil {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			return resourcewrite.Receipt{}, ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.absent {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	if r, ok := w.receipts[i.OperationID]; ok {
		return r, nil
	}
	r := resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", ID: "receipt-" + rand.Text(), OperationID: i.OperationID, IntentHash: i.Hash, Kind: i.Kind, Target: i.Target, Key: i.Key, RecordedAt: time.Now().UTC()}
	if i.ResourceVersion != w.state.Version {
		r.Status = "REJECTED"
		r.Code = "precondition_failed"
	} else {
		r.Status = "APPLIED"
		r.BeforeResourceVersion = w.state.Version
		r.ResourceVersion = fmt.Sprintf("v%d", len(w.receipts)+2)
		r.ItemVersion = "item-" + rand.Text()
		r.ValueHash = i.ValueDigest()
		r.Effect = i.Effect
		w.state.Version = r.ResourceVersion
		w.state.Items = append(w.state.Items, planning.ConfigItem{Key: i.Key, Version: r.ItemVersion, Value: *i.Value})
	}
	w.receipts[i.OperationID] = r
	if w.lost {
		return resourcewrite.Receipt{}, resourcewrite.ErrUnknown
	}
	return r, nil
}
func (w *ledgerWriter) Lookup(_ context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lookups++
	r, ok := w.receipts[i.OperationID]
	if !ok {
		return r, resourcewrite.ErrUnknown
	}
	return r, nil
}
func (w *ledgerWriter) Verify(_ context.Context, i resourcewrite.Intent, r resourcewrite.Receipt) (resourcewrite.Verification, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := planning.Clone(w.state)
	if w.drift {
		state.Version = "outside-change"
	}
	return resourcewrite.VerifyConfig(i, r, state)
}
func (w *ledgerWriter) Read(_ context.Context, t planning.Target) (planning.ConfigState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t != w.state.Target {
		return planning.ConfigState{}, planning.ErrUnavailable
	}
	return planning.Clone(w.state), nil
}

type ledgerReader struct{ writer *ledgerWriter }

func (r ledgerReader) Describe() planning.Descriptor {
	d := r.writer.Describe()
	d.ContractVersion = "config-read.v1"
	return d
}
func (r ledgerReader) ReadConfig(ctx context.Context, t planning.Target) (planning.ConfigState, error) {
	return r.writer.Read(ctx, t)
}

type writePolicy struct {
	writer                            *ledgerWriter
	deny, changed, drift, unavailable atomic.Bool
	checks                            atomic.Int32
}

func (p *writePolicy) Authorize(_ context.Context, subject string, _ delivery.Bundle, op string) (string, error) {
	if p.deny.Load() || subject != "alice" && subject != "bob" || op != "approve_write" && op != "execute_write" && op != "revoke_write" {
		return "", delivery.ErrPolicy
	}
	stamp := subject + ":" + op
	if p.changed.Load() {
		stamp += "-changed"
	}
	return stamp, nil
}
func (p *writePolicy) Writer(_ context.Context, kind string, target planning.Target) (resourcewrite.Writer, error) {
	if kind != "config" || target != p.writer.state.Target {
		return nil, delivery.ErrPolicy
	}
	return p.writer, nil
}
func (p *writePolicy) Revalidate(ctx context.Context, b delivery.Bundle, prefix []resourcewrite.Completed) error {
	p.checks.Add(1)
	if p.unavailable.Load() {
		return delivery.ErrUnavailable
	}
	if p.drift.Load() {
		return delivery.ErrDrift
	}
	planner, err := planning.NewPlanner(map[string]planning.ConfigReader{"config": ledgerReader{p.writer}}, nil, planning.DiffAnalyzer{})
	if err != nil {
		return err
	}
	err = resourcewrite.RevalidateRemaining(ctx, planner, b.Plan, prefix)
	if errors.Is(err, planning.ErrDrift) {
		return delivery.ErrDrift
	}
	return err
}

type writeFixture struct {
	service  *delivery.WriteService
	policy   *writePolicy
	bundle   delivery.Bundle
	approval delivery.WriteApproval
	run      delivery.WriteExecution
	dsn      string
}

func newWriteFixture(t *testing.T, tweaks ...func(*delivery.Bundle)) *writeFixture {
	t.Helper()
	ctx := context.Background()
	dsn := databaseURL(t)
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	r, b := readySimulationFixture("write-test-" + rand.Text())
	for _, tweak := range tweaks {
		tweak(&b)
	}
	a := b.Plan.Actions[0]
	b.Plan.Observations = []planning.Observation{{Kind: a.Kind, Target: a.Target, Descriptor: a.Descriptor, Version: a.ResourceVersion}}
	b.Plan.Hash = ""
	b.Plan.Hash = planning.Hash(b.Plan)
	r.PlanHash = b.Plan.Hash
	saved, _, err := store.Save(ctx, r, b)
	if err != nil {
		t.Fatal(err)
	}
	w := &ledgerWriter{state: planning.ConfigState{Target: a.Target, Version: "v1", SaveMode: "draft", ConditionalUpdates: true, Items: []planning.ConfigItem{}, SecretReferences: []planning.SecretReference{}}, receipts: map[string]resourcewrite.Receipt{}}
	p := &writePolicy{writer: w}
	service := &delivery.WriteService{Store: store, Policy: p}
	f := &writeFixture{service: service, policy: p, bundle: saved, dsn: dsn}
	f.approval, _, err = service.Approve(ctx, delivery.ApprovalInput{TenantID: saved.TenantID, BundleID: saved.ID, Subject: "bob", ExpectedDigest: saved.Digest, IdempotencyKey: "approve", TTLSeconds: 3600, Reason: "reviewed actual writes", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	f.run, _, err = service.Start(ctx, f.startInput())
	if err != nil {
		t.Fatal(err)
	}
	w.inspect = func(i resourcewrite.Intent) error {
		r, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
		if err != nil {
			return err
		}
		for _, o := range r.Operations {
			if o.ID == i.OperationID && o.State == "DISPATCHED" && o.Intent != nil && o.Intent.Hash == i.Hash {
				return nil
			}
		}
		return errors.New("writer invoked before durable intent")
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Error("cleanup database unavailable")
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, `DELETE FROM agent_delivery.write_outbox WHERE tenant_id=$1`, saved.TenantID); err != nil {
			t.Error("cleanup outbox failed")
		}
	})
	return f
}
func (f *writeFixture) startInput() delivery.WriteStartInput {
	return delivery.WriteStartInput{TenantID: f.bundle.TenantID, BundleID: f.bundle.ID, Subject: "alice", IdempotencyKey: "start", ExpectedDigest: f.bundle.Digest, ApprovalID: f.approval.ID}
}
func (f *writeFixture) ref(n int) delivery.Ref {
	return delivery.Ref{TenantID: f.run.TenantID, RunID: f.run.ID, OperationID: f.run.Operations[n].ID}
}
func (f *writeFixture) step(t *testing.T, n int, want bool) {
	t.Helper()
	next, err := f.service.Step(context.Background(), f.ref(n))
	if err != nil || next != want {
		t.Fatalf("step %d: next=%v err=%v", n, next, err)
	}
}
func (f *writeFixture) finish(t *testing.T, want string) {
	t.Helper()
	state, err := f.service.Finish(context.Background(), f.ref(0))
	if err != nil || state != want {
		t.Fatalf("finish: %s %v", state, err)
	}
}

func TestWritePersistsGrantLedgerOutboxAndVerifiedVersionChain(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	a, err := f.service.Store.GetWriteApproval(ctx, f.bundle.TenantID, f.bundle.ID, f.approval.ID)
	if err != nil || a.State != "CONSUMED" || a.ConsumedBy != f.run.ID || a.Mode != "WRITE" || len(f.run.Operations) != 2 {
		t.Fatal("grant not consumed with ledger")
	}
	if _, err := f.service.Store.GetApproval(ctx, f.bundle.TenantID, f.bundle.ID, a.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal("write grant visible as simulation")
	}
	conn, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("database")
	}
	defer func() { _ = conn.Close(ctx) }()
	var count int
	if conn.QueryRow(ctx, `SELECT count(*) FROM agent_delivery.write_outbox WHERE tenant_id=$1 AND run_id=$2`, f.run.TenantID, f.run.ID).Scan(&count) != nil || count != 1 {
		t.Fatal("missing atomic outbox")
	}
	f.step(t, 0, true)
	f.step(t, 0, true)
	f.service.Store.Close()
	store, err := delivery.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f.service.Store = store
	replay, created, err := f.service.Start(ctx, f.startInput())
	if err != nil || created || replay.ID != f.run.ID {
		t.Fatal("lost idempotency after reopen")
	}
	f.step(t, 1, true)
	f.finish(t, "SUCCEEDED")
	r, err := store.GetWriteExecution(ctx, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	if r.Operations[1].Intent.ResourceVersion != r.Operations[0].Receipt.ResourceVersion || r.Operations[1].Intent.OriginalResourceVersion != "v1" || f.policy.writer.calls != 2 {
		t.Fatal("incorrect prefix or repeated Apply")
	}
	input := f.startInput()
	input.IdempotencyKey = "another-start"
	if _, _, err := f.service.Start(ctx, input); !errors.Is(err, delivery.ErrApproval) {
		t.Fatal("grant reused")
	}
	input = f.startInput()
	input.Subject = "bob"
	if _, _, err := f.service.Start(ctx, input); !errors.Is(err, delivery.ErrConflict) {
		t.Fatal("key rebound")
	}
	ref := f.ref(0)
	ref.TenantID = "another-tenant"
	if _, err := store.GetWriteExecution(ctx, ref); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal("tenant escaped")
	}
}

func TestWriteLostResponseReconcilesAfterRevokeWithoutStartingRemainder(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	f.policy.writer.lost = true
	f.step(t, 0, false)
	f.finish(t, "UNKNOWN")
	a, err := f.service.Store.GetWriteApproval(ctx, f.bundle.TenantID, f.bundle.ID, f.approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := delivery.RevokeInput{TenantID: a.TenantID, BundleID: a.BundleID, ApprovalID: a.ID, Subject: "bob", IdempotencyKey: "revoke", ExpectedVersion: a.Version, Reason: "stop"}
	revoked, err := f.service.Revoke(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.Revoke(ctx, in)
	if err != nil || again.Version != revoked.Version {
		t.Fatal("revoke replay")
	}
	in.IdempotencyKey = "replace-revocation"
	in.ExpectedVersion = revoked.Version
	in.Reason = "different reason"
	if _, err := f.service.Revoke(ctx, in); !errors.Is(err, delivery.ErrApproval) {
		t.Fatal("revocation audit overwritten")
	}
	f.policy.deny.Store(true)
	f.service.Store.Close()
	store, err := delivery.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f.service.Store = store
	state, err := f.service.Reconcile(ctx, f.ref(0))
	if err != nil || state != "BLOCKED" {
		t.Fatalf("reconcile: %s %v", state, err)
	}
	f.step(t, 1, false)
	r, err := store.GetWriteExecution(ctx, f.ref(0))
	if err != nil || r.Operations[0].State != "VERIFIED" || r.Operations[1].Intent != nil || f.policy.writer.calls != 1 || f.policy.writer.lookups != 1 {
		t.Fatal("lost committed fact or new effect after revoke")
	}
}

func TestWriteUnknownLookupNeverResendsAndVerificationDriftKeepsReceipt(t *testing.T) {
	for _, mode := range []string{"absent", "drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteFixture(t)
			f.policy.writer.absent = mode == "absent"
			f.policy.writer.drift = mode == "drift"
			f.step(t, 0, false)
			for range 3 {
				if _, err := f.service.Reconcile(context.Background(), f.ref(0)); err != nil {
					t.Fatal(err)
				}
			}
			r, err := f.service.Store.GetWriteExecution(context.Background(), f.ref(0))
			if err != nil {
				t.Fatal(err)
			}
			if f.policy.writer.calls != 1 || r.Operations[1].Intent != nil {
				t.Fatal("unknown/drift resent")
			}
			if mode == "absent" {
				f.finish(t, "UNKNOWN")
				if r.Operations[0].Receipt != nil {
					t.Fatal("fabricated outcome")
				}
			} else {
				f.finish(t, "DRIFTED")
				if r.Operations[0].Receipt == nil || r.Operations[0].Receipt.Status != "APPLIED" {
					t.Fatal("lost applied receipt")
				}
			}
		})
	}
}

func TestWriteBlocksCurrentPolicyAndExternalDriftBetweenActions(t *testing.T) {
	for _, mode := range []string{"permission", "policy", "writer", "code", "resource", "revoke", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteFixture(t)
			ctx := context.Background()
			f.step(t, 0, true)
			switch mode {
			case "permission":
				f.policy.deny.Store(true)
			case "policy":
				f.policy.changed.Store(true)
			case "writer":
				f.policy.writer.version.Store(true)
			case "code":
				f.policy.drift.Store(true)
			case "resource":
				f.policy.writer.state.Version = "outside-change"
			case "revoke":
				a, _ := f.service.Store.GetWriteApproval(ctx, f.bundle.TenantID, f.bundle.ID, f.approval.ID)
				_, err := f.service.Revoke(ctx, delivery.RevokeInput{TenantID: a.TenantID, BundleID: a.BundleID, ApprovalID: a.ID, Subject: "bob", IdempotencyKey: "revoke", ExpectedVersion: a.Version, Reason: "stop"})
				if err != nil {
					t.Fatal(err)
				}
			case "expiry":
				expireWriteGrant(t, f)
			}
			_, err := f.service.Step(ctx, f.ref(1))
			if err != nil && !errors.Is(err, delivery.ErrBusy) {
				t.Fatal(err)
			}
			f.finish(t, "BLOCKED")
			r, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
			if err != nil || r.Operations[0].Receipt == nil || r.Operations[1].Intent != nil || f.policy.writer.calls != 1 {
				t.Fatal("invalidated approval started next write")
			}
		})
	}
}

func expireWriteGrant(t *testing.T, f *writeFixture) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("database")
	}
	defer func() { _ = conn.Close(ctx) }()
	a, err := f.service.Store.GetWriteApproval(ctx, f.bundle.TenantID, f.bundle.ID, f.approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.ExpiresAt = time.Now().Add(-time.Second)
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE agent_delivery.write_approvals SET payload=$3,digest=$4 WHERE tenant_id=$1 AND id=$2`, a.TenantID, a.ID, data, planning.Hash(a)); err != nil {
		t.Fatal("expire fixture")
	}
}

func TestWriteConcurrentStepsOnlyDispatchOnce(t *testing.T) {
	f := newWriteFixture(t)
	w := f.policy.writer
	w.entered = make(chan struct{})
	w.release = make(chan struct{})
	result := make(chan error, 1)
	go func() { _, err := f.service.Step(context.Background(), f.ref(0)); result <- err }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := f.service.Step(context.Background(), f.ref(0))
			if !errors.Is(err, delivery.ErrBusy) {
				t.Errorf("duplicate step: %v", err)
			}
		})
	}
	group.Wait()
	close(w.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 || w.lookups != 0 {
		t.Fatal("duplicate in-flight dispatch")
	}
}
