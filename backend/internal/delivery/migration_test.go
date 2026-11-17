package delivery_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"github.com/jackc/pgx/v5"
)

func TestSimulationMigrationPreservesVersionOneArchives(t *testing.T) {
	for _, version := range []string{"archive-v1", "simulation-v2"} {
		t.Run(version, func(t *testing.T) { checkMigration(t, version) })
	}
}
func checkMigration(t *testing.T, version string) {
	dsn := databaseURL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer func() { _ = admin.Close(ctx) }()
	name := "delivery_migration_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal("create isolated migration database")
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+quoted) }()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	u.Path = "/" + name
	legacy, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("migration database unavailable")
	}
	sql, err := os.ReadFile("migrations/001_archive.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, `CREATE SCHEMA agent_delivery; CREATE TABLE agent_delivery.migrations(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal("create legacy schema")
	}
	if _, err := legacy.Exec(ctx, string(sql)); err != nil {
		t.Fatal("apply v1 schema")
	}
	checksum := planning.Hash(struct{ ChangeID, PlanID, PlanHash string }{ChangeID: string(sql)})
	if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.migrations VALUES(1,$1)`, checksum); err != nil {
		t.Fatal("save v1 checksum")
	}
	r, b := archiveFixture("legacy-tenant")
	b.SchemaVersion = "delivery-archive.v1"
	b.ID = "delivery-legacy"
	b.CreatedAt = time.Now().UTC()
	b.Digest = planning.Hash(b)
	data, _ := json.Marshal(b)
	if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.archives(id,tenant_id,change_id,plan_id,plan_hash,created_at,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, b.ID, b.TenantID, b.ChangeID, b.Plan.ID, b.Plan.Hash, b.CreatedAt, data, b.Digest); err != nil {
		t.Fatal("save legacy archive")
	}
	requestHash := planning.Hash(struct{ ChangeID, PlanID, PlanHash string }{r.ChangeID, r.PlanID, r.PlanHash})
	if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.requests VALUES($1,$2,$3,$4)`, r.TenantID, r.IdempotencyKey, requestHash, b.ID); err != nil {
		t.Fatal("save legacy request")
	}
	if version == "simulation-v2" {
		second, err := os.ReadFile("migrations/002_simulation.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Exec(ctx, string(second)); err != nil {
			t.Fatal("apply v2 schema")
		}
		if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.migrations VALUES(2,$1)`, planning.Hash(string(second))); err != nil {
			t.Fatal("v2 checksum")
		}
		if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.approvals(id,tenant_id,bundle_id,bundle_digest,plan_hash,policy_hash,subject,reason,mode,state,expires_at,idempotency_key,request_hash) VALUES('legacy-approval',$1,$2,$3,$4,'policy','alice','simulation only','SIMULATION','APPROVED',clock_timestamp()+interval '1 hour','old-approval','request')`, b.TenantID, b.ID, b.Digest, b.Plan.Hash); err != nil {
			t.Fatal("v2 approval")
		}
		if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.simulations(id,tenant_id,bundle_id,approval_id,subject,scenario,state,workflow_id,idempotency_key,request_hash) VALUES('legacy-simulation',$1,$2,'legacy-approval','alice','success','QUEUED','legacy-workflow','old-start','request')`, b.TenantID, b.ID); err != nil {
			t.Fatal("v2 simulation")
		}
		if _, err := legacy.Exec(ctx, `INSERT INTO agent_delivery.outbox(id,tenant_id,run_id,kind) VALUES('legacy-outbox',$1,'legacy-simulation','simulation.requested.v1')`, b.TenantID); err != nil {
			t.Fatal("v2 outbox")
		}
	}
	_ = legacy.Close(ctx)
	upgraded, err := delivery.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, found, err := upgraded.Replay(ctx, r)
	if err != nil || !found || got.Digest != b.Digest || got.ID != b.ID {
		t.Fatal("migration changed v1 data or idempotency")
	}
	if version == "simulation-v2" {
		prior, err := upgraded.GetSimulation(ctx, delivery.Ref{TenantID: b.TenantID, RunID: "legacy-simulation"})
		if err != nil || prior.State != "QUEUED" || prior.WorkflowID != "legacy-workflow" {
			t.Fatal("v3 migration changed v2 execution")
		}
		if _, err := upgraded.GetWriteApproval(ctx, b.TenantID, b.ID, "legacy-approval"); err != delivery.ErrNotFound {
			t.Fatal("v2 approval promoted to write")
		}
		jobs, err := upgraded.ClaimOutbox(ctx, "migration-check", 10)
		if err != nil || len(jobs) != 1 || jobs[0].RunID != prior.ID {
			t.Fatal("v2 pending message lost")
		}
	}
	_, a, run := startFixtureSimulation(t, upgraded, "success", 3600)
	if a.Mode != "SIMULATION" || run.State != "QUEUED" {
		t.Fatal("new tables unavailable after migration")
	}
}
