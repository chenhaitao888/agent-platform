package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"github.com/jackc/pgx/v5"
)

func TestWriteApprovalCannotReuseSimulationGrantOrClientPolicyStamp(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	in := delivery.ApprovalInput{TenantID: f.bundle.TenantID, BundleID: f.bundle.ID, ExpectedDigest: f.bundle.Digest, Subject: "alice", PolicyHash: "untrusted", IdempotencyKey: "extra-approval", TTLSeconds: 3600, Reason: "reviewed", FindingsReviewed: true}
	if _, _, err := f.service.Approve(ctx, in); !errors.Is(err, delivery.ErrPolicy) {
		t.Fatal("self-approval enabled implicitly")
	}
	in.Subject = "bob"
	in.FindingsReviewed = false
	if _, _, err := f.service.Approve(ctx, in); !errors.Is(err, delivery.ErrInvalid) {
		t.Fatal("unreviewed findings approved")
	}
	in.FindingsReviewed = true
	a, created, err := f.service.Approve(ctx, in)
	if err != nil || !created || a.PolicyHash == in.PolicyHash {
		t.Fatal("untrusted policy used")
	}
	in.PolicyHash = "another-untrusted-value"
	again, created, err := f.service.Approve(ctx, in)
	if err != nil || created || again.ID != a.ID {
		t.Fatal("approval replay uses caller policy")
	}
	in.Reason = "different"
	if _, _, err := f.service.Approve(ctx, in); !errors.Is(err, delivery.ErrConflict) {
		t.Fatal("approval key rebound")
	}
	in.IdempotencyKey = "simulation-approval"
	in.PolicyHash = "simulated-policy"
	sim, _, err := f.service.Store.Approve(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	start := f.startInput()
	start.IdempotencyKey = "use-simulation"
	start.ApprovalID = sim.ID
	if _, _, err := f.service.Start(ctx, start); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal("simulation approval authorized writes")
	}
	if f.policy.writer.calls != 0 {
		t.Fatal("approval/start invoked provider")
	}
}

func TestWriteConcurrentStartConsumesOnlyOneGrant(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	a, _, err := f.service.Approve(ctx, delivery.ApprovalInput{TenantID: f.bundle.TenantID, BundleID: f.bundle.ID, ExpectedDigest: f.bundle.Digest, Subject: "bob", IdempotencyKey: "parallel-approval", TTLSeconds: 3600, Reason: "reviewed", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	start := f.startInput()
	start.ApprovalID = a.ID
	start.IdempotencyKey = "parallel-start"
	var group sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		group.Go(func() {
			r, _, err := f.service.Start(ctx, start)
			if err != nil {
				results <- "error"
			} else {
				results <- r.ID
			}
		})
	}
	group.Wait()
	close(results)
	ids := map[string]bool{}
	for id := range results {
		ids[id] = true
	}
	if len(ids) != 1 || ids["error"] {
		t.Fatal("concurrent start duplicated/failed")
	}
}

// Simulate a process loss after the durable dispatch marker, before any socket
// write. Recovery cannot prove it was not sent and must still use Lookup only.
func TestWriteCrashBeforeSendCannotTurnMissingReceiptIntoRetry(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	f.policy.writer.inspect = func(resourcewrite.Intent) error { panic("controlled process-loss boundary") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("did not reach persisted dispatch")
			}
		}()
		_, _ = f.service.Step(ctx, f.ref(0))
	}()
	r, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
	if err != nil || r.Operations[0].Intent == nil || r.Operations[0].State != "DISPATCHED" {
		t.Fatal("dispatch marker lost")
	}
	hash := r.Operations[0].Intent.Hash
	mutateWriteRun(t, f, func(r *delivery.WriteExecution) {
		past := time.Now().Add(-time.Second)
		r.Operations[0].LookupAfter = &past
	})
	f.service.Store.Close()
	store, err := delivery.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f.service.Store = store
	f.policy.writer.inspect = nil
	f.step(t, 0, false)
	f.step(t, 0, false)
	f.finish(t, "UNKNOWN")
	r, err = store.GetWriteExecution(ctx, f.ref(0))
	if err != nil || r.Operations[0].Intent.Hash != hash || f.policy.writer.calls != 0 || f.policy.writer.lookups != 2 {
		t.Fatal("crash recovery recreated/replayed intent")
	}
}

func TestWriteRemoteCASFailurePreservesSuccessfulPrefix(t *testing.T) {
	f := newWriteFixture(t)
	f.step(t, 0, true)
	inspect := f.policy.writer.inspect
	f.policy.writer.inspect = func(i resourcewrite.Intent) error {
		if err := inspect(i); err != nil {
			return err
		}
		f.policy.writer.mu.Lock()
		f.policy.writer.state.Version = "changed-after-check"
		f.policy.writer.mu.Unlock()
		return nil
	}
	f.step(t, 1, false)
	f.finish(t, "FAILED")
	r, err := f.service.Store.GetWriteExecution(context.Background(), f.ref(0))
	if err != nil || r.Operations[0].State != "VERIFIED" || r.Operations[1].Receipt == nil || r.Operations[1].Receipt.Code != "precondition_failed" {
		t.Fatal("partial outcomes lost")
	}
	f.step(t, 1, false)
	if f.policy.writer.calls != 2 {
		t.Fatal("rejection retried")
	}
}

func TestWriteRevokedInFlightStillRecordsReceipt(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	w := f.policy.writer
	w.entered = make(chan struct{})
	w.release = make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(w.release) }) })
	done := make(chan error, 1)
	go func() { _, err := f.service.Step(ctx, f.ref(0)); done <- err }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not dispatched")
	}
	a, err := f.service.Store.GetWriteApproval(ctx, f.bundle.TenantID, f.bundle.ID, f.approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.Revoke(ctx, delivery.RevokeInput{TenantID: a.TenantID, BundleID: a.BundleID, ApprovalID: a.ID, Subject: "bob", IdempotencyKey: "stop", ExpectedVersion: a.Version, Reason: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(w.release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.step(t, 1, false)
	f.finish(t, "BLOCKED")
	r, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
	if err != nil || r.Operations[0].State != "VERIFIED" || w.calls != 1 {
		t.Fatal("revocation discarded in-flight outcome")
	}
}

func TestWriteStoredIntentCorruptionFailsClosed(t *testing.T) {
	f := newWriteFixture(t)
	ctx := context.Background()
	f.policy.writer.absent = true
	f.step(t, 0, false)
	conn, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("database")
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE agent_delivery.write_executions SET payload=jsonb_set(payload,'{operations,0,intent,key}','"tampered"') WHERE tenant_id=$1 AND id=$2`, f.run.TenantID, f.run.ID); err != nil {
		t.Fatal("inject corruption")
	}
	if _, err := f.service.Reconcile(ctx, f.ref(0)); !errors.Is(err, delivery.ErrCorrupt) {
		t.Fatal("corrupt intent accepted")
	}
	if f.policy.writer.calls != 1 || f.policy.writer.lookups != 0 {
		t.Fatal("corrupt input reached provider")
	}
}

func TestWriteExpiredGrantStopsEvenWhenReaderIsUnavailable(t *testing.T) {
	f := newWriteFixture(t)
	expireWriteGrant(t, f)
	f.policy.unavailable.Store(true)
	f.step(t, 0, false)
	f.finish(t, "BLOCKED")
	if f.policy.checks.Load() != 0 || f.policy.writer.calls != 0 {
		t.Fatal("expired grant reached provider")
	}
}

func mutateWriteRun(t *testing.T, f *writeFixture, change func(*delivery.WriteExecution)) {
	t.Helper()
	ctx := context.Background()
	r, err := f.service.Store.GetWriteExecution(ctx, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	change(&r)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("database")
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE agent_delivery.write_executions SET payload=$3,digest=$4 WHERE tenant_id=$1 AND id=$2`, r.TenantID, r.ID, data, planning.Hash(r)); err != nil {
		t.Fatal("mutate controlled fixture")
	}
}
