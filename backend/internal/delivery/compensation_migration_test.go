package delivery_test

import (
	"context"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"github.com/jackc/pgx/v5"
)

func TestCompensationMigrationPreservesVersionThreeWriteLedger(t *testing.T) {
	ctx := context.Background()
	dsn := databaseURL(t)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test database")
	}
	defer func() { _ = admin.Close(ctx) }()
	name := "compensation_migration_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal("isolated migration database")
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+quoted) }()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("test URL")
	}
	u.Path = "/" + name
	store, err := delivery.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	request, bundle := readySimulationFixture("migration-write")
	bundle, _, err = store.Save(ctx, request, bundle)
	if err != nil {
		t.Fatal(err)
	}
	// Build valid v3 records through its public service, then remove only the
	// empty v4 table/checksum in this isolated database to model a v3 deployment.
	w := &ledgerWriter{state: planning.ConfigState{Target: bundle.Plan.Actions[0].Target}}
	service := &delivery.WriteService{Store: store, Policy: &writePolicy{writer: w}}
	a, _, err := service.Approve(ctx, delivery.ApprovalInput{TenantID: bundle.TenantID, BundleID: bundle.ID, ExpectedDigest: bundle.Digest, Subject: "bob", IdempotencyKey: "approve", TTLSeconds: 3600, Reason: "v3 approval", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := service.Start(ctx, delivery.WriteStartInput{TenantID: bundle.TenantID, BundleID: bundle.ID, ExpectedDigest: bundle.Digest, Subject: "alice", ApprovalID: a.ID, IdempotencyKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	legacy, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("legacy connection")
	}
	if _, err := legacy.Exec(ctx, `DROP TABLE agent_delivery.compensation_plans; DELETE FROM agent_delivery.migrations WHERE version=4`); err != nil {
		t.Fatal("construct isolated v3 state")
	}
	_ = legacy.Close(ctx)
	upgraded, err := delivery.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.GetWriteExecution(ctx, delivery.Ref{TenantID: run.TenantID, RunID: run.ID})
	if err != nil || got.EvidenceDigest() != run.EvidenceDigest() {
		t.Fatal("v3 ledger changed")
	}
	approval, err := upgraded.GetWriteApproval(ctx, bundle.TenantID, bundle.ID, a.ID)
	if err != nil || approval.State != "CONSUMED" || approval.ConsumedBy != run.ID {
		t.Fatal("v3 grant changed")
	}
	jobs, err := upgraded.ClaimWriteOutbox(ctx, "migration", 10)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != run.ID {
		t.Fatal("v3 outbox lost")
	}
	items, err := upgraded.ListCompensations(ctx, bundle.TenantID, bundle.ID)
	if err != nil || len(items) != 0 {
		t.Fatal("v4 table unavailable")
	}
}
