package delivery_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/task"
	"github.com/jackc/pgx/v5"
)

func databaseURL(t *testing.T) string {
	t.Helper()
	filename := os.Getenv("AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE")
	if filename == "" {
		t.Skip("set AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE for isolated PostgreSQL tests")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal("read test database file failed")
	}
	var config struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("invalid test database file")
	}
	return config.URL
}
func archiveFixture(tenant string) (delivery.Request, delivery.Bundle) {
	reference := task.RepositoryReference{Provider: "gitlab", RepositoryID: "1", TargetBranch: "main", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("c", 40)}
	snapshot := change.Snapshot{ID: "snapshot-1", Revision: 1, State: "READY", InputHash: strings.Repeat("d", 64), Services: []change.ServiceSnapshot{{ServiceID: "S1", CatalogVersion: "1", ConnectorRef: "gitlab", SourceBranch: "feature", TargetBranch: "main", Repository: &reference}}}
	plan := planning.Plan{ID: "plan-1", Revision: 1, SchemaVersion: "change-plan.v1", TenantID: tenant, Environment: "test", SnapshotID: snapshot.ID, SnapshotHash: snapshot.InputHash, CreatedBy: "alice", State: "BLOCKED", CreatedAt: time.Now().UTC(), Services: []planning.Source{{ServiceID: "S1", HeadSHA: reference.HeadSHA, BaseSHA: reference.BaseSHA, Review: planning.ReviewEvidence{State: "NOT_STARTED"}, Candidates: []planning.Candidate{}}}, Actions: []planning.Action{}, Issues: []planning.Issue{{Code: "workspace_required"}}, Observations: []planning.Observation{}}
	plan.Hash = planning.Hash(plan)
	request := delivery.Request{TenantID: tenant, ChangeID: "change-1", PlanID: plan.ID, PlanHash: plan.Hash, IdempotencyKey: "save"}
	bundle := delivery.Bundle{TenantID: tenant, ChangeID: "change-1", Title: "需求 A", Description: "保留待处理计划", Environment: "test", CreatedBy: "alice", Snapshot: snapshot, Plan: plan, Evidence: []delivery.Evidence{}}
	return request, bundle
}
func TestArchiveSurvivesDatabaseReopenAndReplaysWithoutOriginalMemory(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	request, bundle := archiveFixture("archive-test-" + rand.Text())
	first, created, err := store.Save(ctx, request, bundle)
	if err != nil || !created {
		t.Fatalf("save: %v", err)
	}
	store.Close()
	reopened, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(ctx, request.TenantID, first.ID)
	if err != nil || got.Digest != first.Digest || got.Plan.Hash != bundle.Plan.Hash {
		t.Fatalf("saved plan did not survive reopen: %v", err)
	}
	replay, found, err := reopened.Replay(ctx, request)
	if err != nil || !found || replay.ID != first.ID {
		t.Fatal("replay requires lost memory state")
	}
	request.IdempotencyKey = "another-key"
	again, created, err := reopened.Save(ctx, request, bundle)
	if err != nil || created || again.ID != first.ID {
		t.Fatal("same immutable plan was duplicated")
	}
}

func TestArchiveRejectsInvalidSourceAndDetectsCorruption(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, b := archiveFixture("archive-test-" + rand.Text())
	bad := planning.Clone(b)
	bad.Plan.Services[0].HeadSHA = strings.Repeat("f", 40)
	if _, _, err := store.Save(ctx, r, bad); !errors.Is(err, delivery.ErrInvalid) {
		t.Fatal("inconsistent source accepted")
	}
	if _, found, err := store.Replay(ctx, r); err != nil || found {
		t.Fatal("failed validation reserved key")
	}
	ctxCanceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := store.Save(ctxCanceled, r, b); err == nil {
		t.Fatal("canceled save succeeded")
	}
	if _, found, err := store.Replay(ctx, r); err != nil || found {
		t.Fatal("canceled transaction left a request")
	}
	saved, _, err := store.Save(ctx, r, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "other-tenant", saved.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal("tenant isolation failed")
	}
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test database connection failed")
	}
	defer func() { _ = connection.Close(ctx) }()
	if _, err := connection.Exec(ctx, `UPDATE agent_delivery.archives SET payload=jsonb_set(payload,'{title}','"corrupted"') WHERE tenant_id=$1 AND id=$2`, r.TenantID, saved.ID); err != nil {
		t.Fatal("could not inject controlled corruption")
	}
	if _, err := store.Get(ctx, r.TenantID, saved.ID); !errors.Is(err, delivery.ErrCorrupt) {
		t.Fatal("corruption was not detected")
	}
}

func TestArchiveConcurrentNewKeysKeepOneImmutablePackage(t *testing.T) {
	dsn := databaseURL(t)
	ctx := context.Background()
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, b := archiveFixture("archive-test-" + rand.Text())
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			input := r
			input.IdempotencyKey = rand.Text()
			saved, _, err := store.Save(ctx, input, b)
			if err != nil {
				results <- "error"
				return
			}
			results <- saved.ID
		})
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	for id := range results {
		ids[id] = true
	}
	if len(ids) != 1 || ids["error"] {
		t.Fatal("new keys duplicated an immutable plan")
	}
	listed, err := store.List(ctx, r.TenantID)
	if err != nil || len(listed) != 1 {
		t.Fatal("inconsistent durable list")
	}
}

func TestArchiveDatabaseRejectsImplicitAndRemoteConfiguration(t *testing.T) {
	for _, dsn := range []string{"", "postgres://user:short@127.0.0.1:5432/db?sslmode=disable", "postgres://user:long-password-1234@example.invalid:5432/db?sslmode=disable", "postgres://user:long-password-1234@127.0.0.1:5432/db?sslmode=disable&servicefile=/private/file", "postgres://user:long-password-1234@127.0.0.1:5432/db?sslmode=disable&sslmode=disable"} {
		if _, err := delivery.Open(context.Background(), dsn); !errors.Is(err, delivery.ErrConfiguration) {
			t.Fatal("unsafe DSN accepted")
		}
	}
	t.Setenv("PGSERVICE", "do-not-read-service-file")
	if _, err := delivery.Open(context.Background(), "postgres://user:long-password-1234@127.0.0.1:5432/db?sslmode=disable"); !errors.Is(err, delivery.ErrConfiguration) {
		t.Fatal("implicit service configuration accepted")
	}
}
