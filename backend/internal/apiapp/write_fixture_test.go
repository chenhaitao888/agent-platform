package apiapp_test

import (
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
)

// A controlled HTTPS platform in the test process survives API/Worker process
// restarts. Resource mutation and receipt creation share one critical section.
type writeAPIFixture struct {
	mu             sync.Mutex
	config         planning.ConfigState
	experiments    planning.ExperimentState
	receipts       map[string]resourcewrite.Receipt
	effects, posts atomic.Int32
	drop           atomic.Bool
	hold           atomic.Bool
	committed      chan struct{}
	release        chan struct{}
	settings       apiapp.WriteSettings
	filename       string
}

func installWriteFixture(t *testing.T, fixture deploymentFixture) *writeAPIFixture {
	t.Helper()
	planningFixture := installPlanningFixture(t, fixture, true)
	configureArchiveFixture(t, archiveTestFile(t))
	f := &writeAPIFixture{receipts: map[string]resourcewrite.Receipt{}, committed: make(chan struct{}, 1), release: make(chan struct{})}
	configTarget := planning.Target{TenantID: "test", Environment: "test", ConnectorRef: "config-ref", ResourceRef: "shared/settings"}
	experimentTarget := planning.Target{TenantID: "test", Environment: "test", ConnectorRef: "ab-ref", ResourceRef: "checkout-app"}
	f.config = planning.ConfigState{Target: configTarget, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{{Key: "feature.timeout", Version: "item-1", Value: planning.Value{Type: "integer", Text: "100"}}}}
	f.experiments = planning.ExperimentState{Target: experimentTarget, Version: "v1", DraftNoTraffic: true, ConditionalUpdates: true, SupportsExclusion: true, AllocationModes: []string{"weighted"}, Items: []planning.ExperimentItem{}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(f.release) })
	ca := filepath.Join(fixture.directory, "write-ca.pem")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600) != nil {
		t.Fatal("write fixture CA")
	}
	credential := filepath.Join(fixture.directory, "write-token.json")
	writePrivateJSON(t, credential, map[string]string{"token": "write-fixture-token"})
	var readers struct {
		Connectors []map[string]string `json:"connectors"`
	}
	if json.Unmarshal(readLocalFile(t, planningFixture.filename), &readers) != nil {
		t.Fatal("fixture planning")
	}
	address := os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS")
	if address == "" {
		address = "127.0.0.1:17239"
	}
	f.settings = apiapp.WriteSettings{TemporalAddress: address, Namespace: "agent-platform-d4", TaskQueue: "write-api-" + rand.Text(), AllowSelfApproval: true, Connectors: []apiapp.WriteConnectionSettings{}}
	for _, reader := range readers.Connectors {
		reader["baseUrl"] = server.URL
		reader["caFile"] = ca
		target := configTarget
		if reader["kind"] == "experiment" {
			target = experimentTarget
		}
		f.settings.Connectors = append(f.settings.Connectors, apiapp.WriteConnectionSettings{ID: reader["id"], Version: "1", Kind: reader["kind"], BaseURL: server.URL, CAFile: ca, CredentialFile: credential, Targets: []planning.Target{target}})
	}
	writePrivateJSON(t, planningFixture.filename, readers)
	f.filename = filepath.Join(fixture.directory, "writes.json")
	writePrivateJSON(t, f.filename, f.settings)
	t.Setenv("AGENT_PLATFORM_WRITES_FILE", f.filename)
	var catalog servicecatalog.Config
	if json.Unmarshal(readLocalFile(t, os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")), &catalog) != nil {
		t.Fatal("fixture catalog")
	}
	for n := range catalog.Grants {
		catalog.Grants[n].Operations = append(catalog.Grants[n].Operations, "approve_write", "execute_write", "revoke_write")
	}
	writePrivateJSON(t, os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE"), catalog)
	return f
}
func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil || os.WriteFile(path, data, 0o600) != nil {
		t.Fatal("write private fixture JSON")
	}
}

func (f *writeAPIFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer write-fixture-token" && (r.Method != "GET" || r.Header.Get("Authorization") != "Bearer planning-fixture-token") {
		w.WriteHeader(403)
		return
	}
	kind := "config"
	target := f.config.Target
	if strings.HasPrefix(r.URL.Path, "/v1/experiment") {
		kind = "experiment"
		target = f.experiments.Target
	}
	if r.URL.Path != "/v1/"+kind && r.URL.Path != "/v1/"+kind+"/operations" {
		w.WriteHeader(404)
		return
	}
	if r.Method == "GET" {
		q := r.URL.Query()
		if q.Get("tenantId") != target.TenantID || q.Get("environment") != target.Environment || q.Get("resourceRef") != target.ResourceRef {
			w.WriteHeader(404)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/operations") {
			receipt, ok := f.receipts[q.Get("operationId")]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if receipt.IntentHash != q.Get("intentHash") || receipt.Target != target {
				w.WriteHeader(409)
				return
			}
			_ = json.NewEncoder(w).Encode(receipt)
			return
		}
		state := any(f.config)
		if kind == "experiment" {
			state = f.experiments
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": kind + "-read.v1", "state": state})
		return
	}
	if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/operations") {
		w.WriteHeader(405)
		return
	}
	f.posts.Add(1)
	data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	var intent resourcewrite.Intent
	if err != nil || len(data) > 65536 || strictjson.Decode(data, &intent) != nil || !intent.Valid() || intent.Kind != kind || intent.Target != target || r.Header.Get("Idempotency-Key") != intent.OperationID {
		w.WriteHeader(400)
		return
	}
	f.mu.Lock()
	receipt, found := f.receipts[intent.OperationID]
	if found && receipt.IntentHash != intent.Hash {
		f.mu.Unlock()
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]string{"schemaVersion": "resource-write-error.v1", "code": "idempotency_conflict"})
		return
	}
	if !found {
		receipt = f.apply(intent)
		f.receipts[intent.OperationID] = receipt
	}
	f.mu.Unlock()
	select {
	case f.committed <- struct{}{}:
	default:
	}
	if f.hold.Load() {
		select {
		case <-f.release:
		case <-r.Context().Done():
			return
		}
	}
	if f.drop.Swap(false) {
		if h, ok := w.(http.Hijacker); ok {
			conn, _, err := h.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	_ = json.NewEncoder(w).Encode(receipt)
}
func (f *writeAPIFixture) apply(i resourcewrite.Intent) resourcewrite.Receipt {
	r := resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", ID: "receipt-" + rand.Text(), OperationID: i.OperationID, IntentHash: i.Hash, Kind: i.Kind, Target: i.Target, Key: i.Key, RecordedAt: time.Now().UTC(), Status: "REJECTED", Code: "precondition_failed"}
	if !r.RecordedAt.Before(i.ExpiresAt) {
		r.Code = "intent_expired"
		return r
	}
	version := f.config.Version
	itemVersion := ""
	if i.Kind == "config" {
		for _, item := range f.config.Items {
			if item.Key == i.Key {
				itemVersion = item.Version
			}
		}
	} else {
		version = f.experiments.Version
		for _, item := range f.experiments.Items {
			if item.Key == i.Key {
				itemVersion = item.Version
				if item.State != "DRAFT" {
					r.Code = "unsupported"
					return r
				}
			}
		}
	}
	if version != i.ResourceVersion || i.MustNotExist && itemVersion != "" || !i.MustNotExist && itemVersion != i.ExpectedVersion {
		return r
	}
	r.Status = "APPLIED"
	r.Code = ""
	r.BeforeResourceVersion = version
	r.ResourceVersion = fmt.Sprintf("v%d", f.effects.Add(1)+1)
	r.ItemVersion = "item-" + rand.Text()
	r.ValueHash = i.ValueDigest()
	r.Effect = i.Effect
	if i.Kind == "config" {
		item := planning.ConfigItem{Key: i.Key, Version: r.ItemVersion, Value: *i.Value}
		found := false
		for n := range f.config.Items {
			if f.config.Items[n].Key == i.Key {
				f.config.Items[n] = item
				found = true
			}
		}
		if !found {
			f.config.Items = append(f.config.Items, item)
		}
		f.config.Version = r.ResourceVersion
	} else {
		item := planning.ExperimentItem{Key: i.Key, Version: r.ItemVersion, State: "DRAFT", Spec: *i.Experiment}
		found := false
		for n := range f.experiments.Items {
			if f.experiments.Items[n].Key == i.Key {
				f.experiments.Items[n] = item
				found = true
			}
		}
		if !found {
			f.experiments.Items = append(f.experiments.Items, item)
		}
		f.experiments.Version = r.ResourceVersion
	}
	return r
}
