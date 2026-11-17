package reference_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This provider exists only in tests. Its two storage implementations exercise
// the same HTTP contract; PostgreSQL commits resources and receipts together.
type writeDatabase struct {
	Config      planning.ConfigState     `json:"config"`
	Experiments planning.ExperimentState `json:"experiments"`
	Receipts    []resourcewrite.Receipt  `json:"receipts"`
	Effects     int                      `json:"effects"`
}
type writeBackend interface {
	transaction(context.Context, func(*writeDatabase) error) error
}
type memoryWriteBackend struct {
	mu   sync.Mutex
	data writeDatabase
}

func (b *memoryWriteBackend) transaction(ctx context.Context, fn func(*writeDatabase) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	copy := planning.Clone(b.data)
	if err := fn(&copy); err != nil {
		return err
	}
	b.data = copy
	return nil
}

type postgresWriteBackend struct {
	pool               *pgxpool.Pool
	dsn, table, schema string
}

func (b *postgresWriteBackend) transaction(ctx context.Context, fn func(*writeDatabase) error) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM "+b.table+" WHERE id=1 FOR UPDATE").Scan(&raw); err != nil {
		return err
	}
	var data writeDatabase
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if err := fn(&data); err != nil {
		return err
	}
	raw, err = json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE "+b.table+" SET payload=$1 WHERE id=1", raw); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func newPostgresWriteBackend(t *testing.T, data writeDatabase) *postgresWriteBackend {
	t.Helper()
	filename := os.Getenv("AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE")
	if filename == "" {
		t.Skip("set AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE for durable reference platform tests")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal("test database settings unavailable")
	}
	var settings struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		t.Fatal("invalid test database settings")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, settings.URL)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	schema := "reference_write_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	b := &postgresWriteBackend{pool: pool, dsn: settings.URL, schema: quoted, table: quoted + ".state"}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := b.pool.Exec(ctx, "DROP SCHEMA "+b.schema+" CASCADE"); err != nil {
			t.Error("reference schema cleanup failed")
		}
		b.pool.Close()
	})
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted+"; CREATE TABLE "+b.table+"(id integer PRIMARY KEY,payload jsonb NOT NULL)"); err != nil {
		t.Fatal("reference schema initialization failed")
	}
	raw, _ = json.Marshal(data)
	if _, err := pool.Exec(ctx, "INSERT INTO "+b.table+"(id,payload) VALUES(1,$1)", raw); err != nil {
		t.Fatal("reference data initialization failed")
	}
	return b
}
func (b *postgresWriteBackend) reopen(t *testing.T) {
	t.Helper()
	b.pool.Close()
	pool, err := pgxpool.New(context.Background(), b.dsn)
	if err != nil {
		t.Fatal("reference restart failed")
	}
	b.pool = pool
}

var configTarget = planning.Target{TenantID: "tenant", Environment: "test", ConnectorRef: "config-ref", ResourceRef: "shared/settings"}
var experimentTarget = planning.Target{TenantID: "tenant", Environment: "test", ConnectorRef: "ab-ref", ResourceRef: "checkout"}

func initialWriteDatabase() writeDatabase {
	return writeDatabase{Config: planning.ConfigState{Target: configTarget, Version: "resource-1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{{Key: "checkout.limit", Version: "item-1", Value: planning.Value{Type: "integer", Text: "10"}}}, SecretReferences: []planning.SecretReference{{Ref: "vault/service", Versions: []string{"v1"}}}}, Experiments: planning.ExperimentState{Target: experimentTarget, Version: "resource-1", DraftNoTraffic: true, ConditionalUpdates: true, SupportsExclusion: true, AllocationModes: []string{"weighted"}, Items: []planning.ExperimentItem{}}, Receipts: []resourcewrite.Receipt{}}
}

type writePlatform struct {
	backend writeBackend
	server  *httptest.Server
	calls   atomic.Int32
	drop    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newWritePlatform(t *testing.T, backend writeBackend) *writePlatform {
	t.Helper()
	p := &writePlatform{backend: backend}
	p.server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

var errFixtureConflict = errors.New("fixture idempotency conflict")
var errFixtureMissing = errors.New("fixture operation not recorded")

func (p *writePlatform) serve(w http.ResponseWriter, r *http.Request) {
	p.calls.Add(1)
	if r.Header.Get("Authorization") != "Bearer writer-fixture-token" {
		w.WriteHeader(403)
		return
	}
	kind := "config"
	target := configTarget
	if strings.HasPrefix(r.URL.Path, "/v1/experiment") {
		kind = "experiment"
		target = experimentTarget
	}
	if r.URL.Path != "/v1/"+kind && r.URL.Path != "/v1/"+kind+"/operations" {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		q := r.URL.Query()
		if q.Get("tenantId") != target.TenantID || q.Get("environment") != target.Environment || q.Get("resourceRef") != target.ResourceRef {
			w.WriteHeader(404)
			return
		}
		var result any
		err := p.backend.transaction(r.Context(), func(data *writeDatabase) error {
			if strings.HasSuffix(r.URL.Path, "/operations") {
				for _, receipt := range data.Receipts {
					if receipt.Target != target || receipt.Kind != kind || receipt.OperationID != q.Get("operationId") {
						continue
					}
					if receipt.IntentHash != q.Get("intentHash") {
						return errFixtureConflict
					}
					result = receipt
					return nil
				}
				return errFixtureMissing
			}
			state := any(data.Config)
			if kind == "experiment" {
				state = data.Experiments
			}
			result = map[string]any{"schemaVersion": kind + "-read.v1", "state": state}
			return nil
		})
		if err != nil {
			fixtureHTTPError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/operations") {
		w.WriteHeader(405)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	var in resourcewrite.Intent
	if err != nil || len(raw) > 64<<10 || strictjson.Decode(raw, &in) != nil || !in.Valid() || in.Kind != kind || in.Target != target || r.Header.Get("Idempotency-Key") != in.OperationID {
		w.WriteHeader(400)
		return
	}
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		<-p.release
	}
	var receipt resourcewrite.Receipt
	err = p.backend.transaction(r.Context(), func(data *writeDatabase) error {
		for _, prior := range data.Receipts {
			if prior.Target.TenantID != in.Target.TenantID || prior.OperationID != in.OperationID {
				continue
			}
			if prior.IntentHash != in.Hash {
				return errFixtureConflict
			}
			receipt = prior
			return nil
		}
		receipt = decideWrite(data, in)
		data.Receipts = append(data.Receipts, receipt)
		return nil
	})
	if err != nil {
		fixtureHTTPError(w, err)
		return
	}
	if p.drop.CompareAndSwap(true, false) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
		return
	}
	_ = json.NewEncoder(w).Encode(receipt)
}
func fixtureHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, errFixtureConflict) {
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]string{"schemaVersion": "resource-write-error.v1", "code": "idempotency_conflict"})
		return
	}
	if errors.Is(err, errFixtureMissing) {
		w.WriteHeader(404)
		return
	}
	w.WriteHeader(503)
}
func decideWrite(data *writeDatabase, in resourcewrite.Intent) resourcewrite.Receipt {
	r := resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", ID: "receipt-" + rand.Text(), OperationID: in.OperationID, IntentHash: in.Hash, Kind: in.Kind, Target: in.Target, Key: in.Key, Status: "REJECTED", RecordedAt: time.Now().UTC()}
	reject := func(code string) resourcewrite.Receipt { r.Code = code; return r }
	if !r.RecordedAt.Before(in.ExpiresAt) {
		return reject("intent_expired")
	}
	var itemVersion, resourceVersion string
	if in.Kind == "config" {
		state := &data.Config
		if state.Version != in.ResourceVersion {
			return reject("precondition_failed")
		}
		index := slices.IndexFunc(state.Items, func(i planning.ConfigItem) bool { return i.Key == in.Key })
		if in.MustNotExist != (index < 0) || (index >= 0 && state.Items[index].Version != in.ExpectedVersion) {
			return reject("precondition_failed")
		}
		if !state.ConditionalUpdates || state.SaveMode != in.Effect {
			return reject("unsupported")
		}
		if index >= 0 && state.Items[index].Value.Type != in.Value.Type {
			return reject("value_rejected")
		}
		if index >= 0 {
			rules := state.Items[index].Constraints
			if len(rules.Allowed) > 0 && !slices.Contains(rules.Allowed, in.Value.Text) {
				return reject("value_rejected")
			}
			if in.Value.Type == "integer" || in.Value.Type == "number" {
				value, _ := strconv.ParseFloat(in.Value.Text, 64)
				if (rules.Min != nil && value < *rules.Min) || (rules.Max != nil && value > *rules.Max) {
					return reject("value_rejected")
				}
			}
		}
		if in.Value.Type == "secretRef" {
			if !slices.ContainsFunc(state.SecretReferences, func(ref planning.SecretReference) bool {
				return ref.Ref == in.Value.SecretRef && slices.Contains(ref.Versions, in.Value.SecretVersion)
			}) {
				return reject("value_rejected")
			}
		}
		if in.Operation == "config.no_change" {
			if index < 0 || state.Items[index].Value != *in.Value {
				return reject("value_rejected")
			}
			itemVersion = state.Items[index].Version
			resourceVersion = state.Version
		} else {
			data.Effects++
			itemVersion = fmt.Sprintf("item-%d", data.Effects+1)
			resourceVersion = fmt.Sprintf("resource-%d", data.Effects+1)
			item := planning.ConfigItem{Key: in.Key, Version: itemVersion, Value: *in.Value, Sensitive: in.Value.Type == "secretRef"}
			if index < 0 {
				state.Items = append(state.Items, item)
			} else {
				item.Constraints = state.Items[index].Constraints
				state.Items[index] = item
			}
			state.Version = resourceVersion
		}
	} else {
		state := &data.Experiments
		if state.Version != in.ResourceVersion {
			return reject("precondition_failed")
		}
		index := slices.IndexFunc(state.Items, func(i planning.ExperimentItem) bool { return i.Key == in.Key })
		if in.MustNotExist != (index < 0) || (index >= 0 && state.Items[index].Version != in.ExpectedVersion) {
			return reject("precondition_failed")
		}
		if !state.DraftNoTraffic || !state.ConditionalUpdates || !slices.Contains(state.AllocationModes, in.Experiment.Allocation) || (in.Experiment.ExclusionGroup != "" && !state.SupportsExclusion) {
			return reject("unsupported")
		}
		if index >= 0 && state.Items[index].State != "DRAFT" {
			return reject("value_rejected")
		}
		data.Effects++
		itemVersion = fmt.Sprintf("item-%d", data.Effects+1)
		resourceVersion = fmt.Sprintf("resource-%d", data.Effects+1)
		item := planning.ExperimentItem{Key: in.Key, Version: itemVersion, State: "DRAFT", Spec: planning.Clone(*in.Experiment)}
		if index < 0 {
			state.Items = append(state.Items, item)
		} else {
			state.Items[index] = item
		}
		state.Version = resourceVersion
	}
	r.Status = "APPLIED"
	r.BeforeResourceVersion = in.ResourceVersion
	r.ResourceVersion = resourceVersion
	r.ItemVersion = itemVersion
	r.ValueHash = in.ValueDigest()
	r.Effect = in.Effect
	return r
}
func snapshotWriteBackend(t *testing.T, b writeBackend) writeDatabase {
	t.Helper()
	var snapshot writeDatabase
	if b.transaction(context.Background(), func(d *writeDatabase) error { snapshot = planning.Clone(*d); return nil }) != nil {
		t.Fatal("read reference state")
	}
	return snapshot
}
