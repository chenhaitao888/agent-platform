package delivery_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/review"
	"github.com/jackc/pgx/v5"
)

func TestSimulationApprovalAndOutboxSurviveReopen(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, b := readySimulationFixture("simulation-test-" + rand.Text())
	saved, _, err := store.Save(ctx, r, b)
	if err != nil {
		t.Fatal(err)
	}
	approval, created, err := store.Approve(ctx, delivery.ApprovalInput{TenantID: r.TenantID, BundleID: saved.ID, ExpectedDigest: saved.Digest, Subject: "alice", PolicyHash: "trusted-policy", IdempotencyKey: "approval", TTLSeconds: 3600, Reason: "已复核需求与 Findings，仅批准模拟", FindingsReviewed: true})
	if err != nil || !created || approval.Mode != "SIMULATION" {
		t.Fatalf("approve: %v", err)
	}
	input := delivery.StartInput{TenantID: r.TenantID, BundleID: saved.ID, ApprovalID: approval.ID, ExpectedDigest: saved.Digest, Subject: "alice", IdempotencyKey: "start", Scenario: "response_loss"}
	run, created, err := store.StartSimulation(ctx, input)
	if err != nil || !created || run.State != "QUEUED" {
		t.Fatalf("start: %v", err)
	}
	store.Close()
	reopened, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, created, err := reopened.StartSimulation(ctx, input)
	if err != nil || created || replay.ID != run.ID {
		t.Fatal("restart lost start idempotency")
	}
	// Other tests may leave older pending jobs. Verify this transaction's row
	// directly, without assuming a global dispatcher batch contains this run.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("read outbox fixture")
	}
	defer func() { _ = conn.Close(ctx) }()
	var messages int
	if conn.QueryRow(ctx, `SELECT count(*) FROM agent_delivery.outbox WHERE tenant_id=$1 AND run_id=$2 AND kind='simulation.requested.v1'`, r.TenantID, run.ID).Scan(&messages) != nil || messages != 1 {
		t.Fatal("committed run has no durable outbox message")
	}
}

type simulationPolicy struct{ revoked, drift atomic.Bool }

func (p *simulationPolicy) Authorize(context.Context, string, delivery.Bundle, string) (string, error) {
	if p.revoked.Load() {
		return "", delivery.ErrPolicy
	}
	return "trusted-policy", nil
}
func (p *simulationPolicy) Revalidate(context.Context, delivery.Bundle) error {
	if p.drift.Load() {
		return delivery.ErrDrift
	}
	return nil
}
func startFixtureSimulation(t *testing.T, store *delivery.Store, scenario string, ttl int) (delivery.Bundle, delivery.Approval, delivery.Simulation) {
	t.Helper()
	ctx := context.Background()
	r, b := readySimulationFixture("simulation-test-" + rand.Text())
	saved, _, err := store.Save(ctx, r, b)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := store.Approve(ctx, delivery.ApprovalInput{TenantID: r.TenantID, BundleID: saved.ID, ExpectedDigest: saved.Digest, Subject: "alice", PolicyHash: "trusted-policy", IdempotencyKey: rand.Text(), TTLSeconds: ttl, Reason: "fixture approval", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.StartSimulation(ctx, delivery.StartInput{TenantID: r.TenantID, BundleID: saved.ID, ApprovalID: a.ID, ExpectedDigest: saved.Digest, Subject: "alice", IdempotencyKey: rand.Text(), Scenario: scenario})
	if err != nil {
		t.Fatal(err)
	}
	return saved, a, run
}
func receiptCount(t *testing.T, dsn string, ref delivery.Ref) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect receipt fixture")
	}
	defer func() { _ = conn.Close(ctx) }()
	var count int
	if conn.QueryRow(ctx, `SELECT count(*) FROM agent_delivery.simulation_receipts r JOIN agent_delivery.operations o ON o.tenant_id=r.tenant_id AND o.id=r.operation_id WHERE o.tenant_id=$1 AND o.run_id=$2`, ref.TenantID, ref.RunID).Scan(&count) != nil {
		t.Fatal("read receipts failed")
	}
	return count
}
func TestSimulationReconcilesLostReplyBeforeHonoringRevocationForNewWork(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b, a, run := startFixtureSimulation(t, store, "response_loss", 3600)
	policy := &simulationPolicy{}
	service := delivery.NewSimulationService(store, policy, "worker-a", 2)
	ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID}
	ids, err := service.Begin(ctx, ref)
	if err != nil || len(ids) != 2 {
		t.Fatalf("begin: %v", err)
	}
	step := ref
	step.OperationID = ids[0]
	if _, err := service.Step(ctx, step); !errors.Is(err, delivery.ErrRetry) {
		t.Fatalf("lost response was not recorded: %v", err)
	}
	op, err := store.GetOperation(ctx, step)
	if err != nil || op.State != "UNKNOWN" || receiptCount(t, dsn, ref) != 1 {
		t.Fatal("effect/ledger gap was lost")
	}
	current, err := store.GetApproval(ctx, b.TenantID, b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(ctx, delivery.RevokeInput{TenantID: b.TenantID, BundleID: b.ID, ApprovalID: a.ID, Subject: "alice", IdempotencyKey: "revoke", ExpectedVersion: current.Version, Reason: "stop new work"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := delivery.NewSimulationService(reopened, policy, "worker-b", 2)
	if next, err := resumed.Step(ctx, step); err != nil || !next {
		t.Fatal("revocation hid an already committed receipt")
	}
	step.OperationID = ids[1]
	if next, err := resumed.Step(ctx, step); err != nil || next {
		t.Fatal("revoked approval started new work")
	}
	state, err := reopened.FinishSimulation(ctx, ref)
	if err != nil || state != "BLOCKED" || receiptCount(t, dsn, ref) != 1 {
		t.Fatal("recovery duplicated work or reported success")
	}
}
func TestSimulationBlocksExpiredPermissionAndResourceDrift(t *testing.T) {
	for _, scenario := range []string{"expired", "permission", "drift"} {
		t.Run(scenario, func(t *testing.T) {
			dsn := databaseURL(t)
			ctx := context.Background()
			store, err := delivery.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ttl := 3600
			if scenario == "expired" {
				ttl = 1
			}
			b, _, run := startFixtureSimulation(t, store, "success", ttl)
			policy := &simulationPolicy{}
			if scenario == "permission" {
				policy.revoked.Store(true)
			}
			if scenario == "drift" {
				policy.drift.Store(true)
			}
			if scenario == "expired" {
				time.Sleep(1100 * time.Millisecond)
			}
			service := delivery.NewSimulationService(store, policy, "worker", 2)
			ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID}
			ids, err := service.Begin(ctx, ref)
			if err != nil || len(ids) != 0 {
				t.Fatal("invalid approval began operations")
			}
			state, err := store.FinishSimulation(ctx, ref)
			if err != nil || state != "BLOCKED" || receiptCount(t, dsn, ref) != 0 {
				t.Fatal("blocked simulation created effect")
			}
		})
	}
}
func TestSimulationLeaseFencesLateWritersAndStopsAfterPartialFailure(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b, _, run := startFixtureSimulation(t, store, "success", 3600)
	service := delivery.NewSimulationService(store, &simulationPolicy{}, "worker", 2)
	ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID}
	ids, err := service.Begin(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	step := ref
	step.OperationID = ids[0]
	old, err := store.ClaimOperation(ctx, step, "old", 1)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	newer, err := store.ClaimOperation(ctx, step, "new", 30)
	if err != nil || newer.Fence <= old.Fence {
		t.Fatal("lease did not advance fence")
	}
	if _, err := store.SimulateEffect(ctx, step, old.Fence); !errors.Is(err, delivery.ErrLeaseLost) {
		t.Fatal("stale writer simulated effect")
	}
	if err := store.FailOperation(ctx, step, old.Fence, "late_error"); !errors.Is(err, delivery.ErrLeaseLost) {
		t.Fatal("stale writer overwrote result")
	}
	if _, err := store.SimulateEffect(ctx, step, newer.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileOperation(ctx, step); err != nil {
		t.Fatal(err)
	}
	step.OperationID = ids[1]
	claimed, err := store.ClaimOperation(ctx, step, "current", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailOperation(ctx, step, claimed.Fence, "simulated_failure"); err != nil {
		t.Fatal(err)
	}
	state, err := store.FinishSimulation(ctx, ref)
	if err != nil || state != "FAILED" || receiptCount(t, dsn, ref) != 1 {
		t.Fatal("partial failure lost successful effect")
	}
	stored, err := store.GetSimulation(ctx, ref)
	if err != nil || stored.Operations[0].State != "SUCCEEDED" || stored.Operations[1].State != "FAILED" {
		t.Fatal("partial states overwritten")
	}
}

func readySimulationFixture(tenant string) (delivery.Request, delivery.Bundle) {
	r, b := archiveFixture(tenant)
	source := &b.Plan.Services[0]
	source.Review = planning.ReviewEvidence{TaskID: "task-1", ExecutionID: "review-1", ArtifactID: "artifact-1", State: "SUCCEEDED"}
	source.ConfigCoverage = "CANDIDATES_FOUND"
	source.ExperimentCoverage = "NOT_INVOLVED"
	source.ExperimentReason = "no experiment change"
	content, _ := json.Marshal(review.FindingsReport{SchemaVersion: "1.0", BaseSHA: source.BaseSHA, HeadSHA: source.HeadSHA, Findings: []review.Finding{}})
	metadata := artifact.Artifact{ID: "artifact-1", TenantID: tenant, TaskID: "task-1", WorkspaceID: "workspace-1", Type: artifact.TypePRReviewFindings, MediaType: "application/json", SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC()}
	b.Evidence = []delivery.Evidence{{ServiceID: "S1", Execution: review.Execution{ID: "review-1", TenantID: tenant, TaskID: "task-1", WorkspaceID: "workspace-1", WorkspaceVersion: 3, BaseSHA: source.BaseSHA, HeadSHA: source.HeadSHA, State: review.ExecutionSucceeded, Artifact: &metadata}, Content: content}}
	b.Plan.State = "READY_FOR_REVIEW"
	b.Plan.Issues = []planning.Issue{}
	b.Plan.Actions = []planning.Action{}
	for i := range 2 {
		a := planning.Action{ID: fmt.Sprintf("action-%d", i+1), Kind: "config", Key: fmt.Sprintf("setting-%d", i+1), Operation: "config.create", Target: planning.Target{TenantID: tenant, Environment: "test", ConnectorRef: "config", ResourceRef: "settings"}, Descriptor: planning.Descriptor{Kind: "config", ContractVersion: "config-read.v1", ImplementationVersion: "fixture.v1", ConnectionVersion: "1"}, ResourceVersion: "v1", MustNotExist: true, Effect: "draft", Proposals: []planning.Proposal{{ServiceID: "S1", Value: &planning.Value{Type: "boolean", Text: "false"}, Evidence: []planning.Evidence{{Source: "user-requirement", ServiceID: "S1", HeadSHA: source.HeadSHA, Reason: "fixture requirement"}}}}}
		a.Hash = planning.Hash(a)
		b.Plan.Actions = append(b.Plan.Actions, a)
	}
	b.Plan.Hash = ""
	b.Plan.Hash = planning.Hash(b.Plan)
	r.PlanHash = b.Plan.Hash
	return r, b
}
