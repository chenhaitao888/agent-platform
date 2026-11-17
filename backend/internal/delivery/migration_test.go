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
	_, a, run := startFixtureSimulation(t, upgraded, "success", 3600)
	if a.Mode != "SIMULATION" || run.State != "QUEUED" {
		t.Fatal("new tables unavailable after migration")
	}
}
