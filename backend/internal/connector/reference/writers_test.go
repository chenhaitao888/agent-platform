package reference_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/connector/reference"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

func writePlan() planning.Plan {
	plan := planning.Plan{SchemaVersion: "change-plan.v1", TenantID: "tenant", Environment: "test", State: "READY_FOR_REVIEW", Actions: []planning.Action{}}
	for _, key := range []string{"checkout.limit", "checkout.enabled", "checkout-test"} {
		a := planning.Action{ID: key, Kind: "config", Target: configTarget, Key: key, Operation: "config.create", Effect: "publish_required", ResourceVersion: "resource-1", MustNotExist: true, Descriptor: planning.Descriptor{Kind: "config", ContractVersion: "config-read.v1", ImplementationVersion: "reference-http.v1", ConnectionVersion: "1"}, Proposals: []planning.Proposal{{ServiceID: "S1", Value: &planning.Value{Type: "boolean", Text: "false"}}}}
		if key == "checkout.limit" {
			a.Operation = "config.update"
			a.MustNotExist = false
			a.ExpectedVersion = "item-1"
			a.CurrentValue = &planning.Value{Type: "integer", Text: "10"}
			a.Proposals[0].Value = &planning.Value{Type: "integer", Text: "20"}
		}
		if key == "checkout-test" {
			a.Kind = "experiment"
			a.Target = experimentTarget
			a.Operation = "experiment.create_draft"
			a.Effect = "inactive_draft"
			a.Descriptor.Kind = "experiment"
			a.Descriptor.ContractVersion = "experiment-read.v1"
			a.Proposals[0].Value = nil
			a.Proposals[0].Experiment = &planning.ExperimentSpec{Variants: []planning.Variant{{Name: "control", Weight: 50}, {Name: "treatment", Weight: 50}}, Audience: "test-users", Metrics: []string{"conversion"}, Allocation: "weighted"}
		}
		a.Hash = planning.Hash(a)
		plan.Actions = append(plan.Actions, a)
	}
	plan.Hash = planning.Hash(plan)
	return plan
}
func writerFor(t *testing.T, p *writePlatform, kind string) resourcewrite.Writer {
	t.Helper()
	target := configTarget
	if kind == "experiment" {
		target = experimentTarget
	}
	c := reference.WriteConnection{Connection: reference.Connection{ID: target.ConnectorRef, Version: "1", BaseURL: p.server.URL, Token: "writer-fixture-token"}, Targets: []planning.Target{target}}
	var w resourcewrite.Writer
	var err error
	if kind == "config" {
		w, err = reference.NewConfigWriter(c, p.server.Client())
	} else {
		w, err = reference.NewExperimentWriter(c, p.server.Client())
	}
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func prepareWrite(t *testing.T, plan planning.Plan, n int, w resourcewrite.Writer, prefix []resourcewrite.Completed) resourcewrite.Intent {
	t.Helper()
	i, err := resourcewrite.Prepare(plan, n, "operation-"+plan.Actions[n].ID, w.Describe(), time.Now().UTC().Add(time.Hour), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func executeVerified(t *testing.T, w resourcewrite.Writer, i resourcewrite.Intent) resourcewrite.Completed {
	t.Helper()
	r, err := w.Apply(context.Background(), i)
	if err != nil || r.Status != "APPLIED" {
		t.Fatalf("apply: %+v %v", r, err)
	}
	v, err := w.Verify(context.Background(), i, r)
	if err != nil || !v.ValidFor(i, r) {
		t.Fatalf("verify: %+v %v", v, err)
	}
	return resourcewrite.Completed{Intent: i, Receipt: r, Verification: v}
}

func TestReferenceWriteContractAcrossMemoryAndDurableProviders(t *testing.T) {
	for _, storage := range []string{"memory", "postgres"} {
		t.Run(storage, func(t *testing.T) {
			var backend writeBackend = &memoryWriteBackend{data: initialWriteDatabase()}
			if storage == "postgres" {
				backend = newPostgresWriteBackend(t, initialWriteDatabase())
			}
			platform := newWritePlatform(t, backend)
			config := writerFor(t, platform, "config")
			experiment := writerFor(t, platform, "experiment")
			plan := writePlan()
			prefix := []resourcewrite.Completed{}
			for n, a := range plan.Actions {
				w := config
				if a.Kind == "experiment" {
					w = experiment
				}
				i := prepareWrite(t, plan, n, w, prefix)
				if n == 1 && i.ResourceVersion == i.OriginalResourceVersion {
					t.Fatal("shared resource version was not advanced")
				}
				done := executeVerified(t, w, i)
				prefix = append(prefix, done)
				replay, err := w.Apply(context.Background(), i)
				if err != nil || planning.Hash(replay) != planning.Hash(done.Receipt) {
					t.Fatal("duplicate changed receipt")
				}
			}
			data := snapshotWriteBackend(t, backend)
			if data.Effects != 3 || len(data.Receipts) != 3 || data.Config.Items[0].Value.Text != "20" || len(data.Experiments.Items) != 1 || data.Experiments.Items[0].State != "DRAFT" {
				t.Fatalf("unexpected effects: %+v", data)
			}
			// Restart the durable provider's connection and HTTP server, retaining only PostgreSQL.
			if durable, ok := backend.(*postgresWriteBackend); ok {
				platform.server.Close()
				durable.reopen(t)
				platform = newWritePlatform(t, durable)
				config = writerFor(t, platform, "config")
				experiment = writerFor(t, platform, "experiment")
			}
			for _, done := range prefix {
				w := config
				if done.Intent.Kind == "experiment" {
					w = experiment
				}
				receipt, err := w.Lookup(context.Background(), done.Intent)
				if err != nil || planning.Hash(receipt) != planning.Hash(done.Receipt) {
					t.Fatal("receipt was not retained")
				}
			}
			changed := prefix[0].Intent
			changed.Value = &planning.Value{Type: "integer", Text: "99"}
			changed.Hash = changed.Digest()
			if _, err := config.Apply(context.Background(), changed); !errors.Is(err, resourcewrite.ErrConflict) {
				t.Fatal("operation ID accepted a different payload")
			}
			if snapshotWriteBackend(t, backend).Effects != 3 {
				t.Fatal("replay/restart duplicated effects")
			}
		})
	}
}

func TestCommittedWriteWithLostResponseIsLookedUpWithoutAnotherPost(t *testing.T) {
	backend := newPostgresWriteBackend(t, initialWriteDatabase())
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	intent := prepareWrite(t, writePlan(), 0, writer, nil)
	platform.drop.Store(true)
	if _, err := writer.Apply(context.Background(), intent); !errors.Is(err, resourcewrite.ErrUnknown) {
		t.Fatalf("lost response: %v", err)
	}
	if platform.calls.Load() != 1 || snapshotWriteBackend(t, backend).Effects != 1 {
		t.Fatal("POST was retried or effect not committed")
	}
	platform.server.Close()
	backend.reopen(t)
	platform = newWritePlatform(t, backend)
	writer = writerFor(t, platform, "config")
	receipt, err := writer.Lookup(context.Background(), intent)
	if err != nil || receipt.Status != "APPLIED" {
		t.Fatalf("reconcile: %+v %v", receipt, err)
	}
	if v, err := writer.Verify(context.Background(), intent, receipt); err != nil || !v.ValidFor(intent, receipt) {
		t.Fatal("committed write not verified")
	}
	if platform.calls.Load() != 2 || snapshotWriteBackend(t, backend).Effects != 1 {
		t.Fatal("reconciliation wrote again")
	}
}

func TestReferenceConcurrentCASAndStableRejectedReceipts(t *testing.T) {
	backend := &memoryWriteBackend{data: initialWriteDatabase()}
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	first := prepareWrite(t, writePlan(), 0, writer, nil)
	second := planning.Clone(first)
	second.OperationID = "other-operation"
	second.Value.Text = "30"
	second.Hash = second.Digest()
	var results [2]resourcewrite.Receipt
	var failures [2]error
	var group sync.WaitGroup
	for n, intent := range []resourcewrite.Intent{first, second} {
		group.Add(1)
		go func() { defer group.Done(); results[n], failures[n] = writer.Apply(context.Background(), intent) }()
	}
	group.Wait()
	applied, rejected := 0, 0
	for n, r := range results {
		if failures[n] != nil {
			t.Fatal(failures[n])
		}
		if r.Status == "APPLIED" {
			applied++
		} else if r.Status == "REJECTED" && r.Code == "precondition_failed" {
			rejected++
		} else {
			t.Fatalf("bad result: %+v", r)
		}
	}
	if applied != 1 || rejected != 1 || snapshotWriteBackend(t, backend).Effects != 1 {
		t.Fatal("CAS did not serialize competing writers")
	}
	for n, intent := range []resourcewrite.Intent{first, second} {
		r, err := writer.Apply(context.Background(), intent)
		if err != nil || planning.Hash(r) != planning.Hash(results[n]) {
			t.Fatal("rejected/successful outcome changed on replay")
		}
	}
}

func TestReferenceDuplicateConcurrentPostsCommitOnce(t *testing.T) {
	backend := newPostgresWriteBackend(t, initialWriteDatabase())
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	intent := prepareWrite(t, writePlan(), 0, writer, nil)
	results := make(chan resourcewrite.Receipt, 8)
	errs := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			r, err := writer.Apply(context.Background(), intent)
			results <- r
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	prior := ""
	for r := range results {
		if r.Status != "APPLIED" || (prior != "" && prior != r.ID) {
			t.Fatal("duplicate committed a different result")
		}
		prior = r.ID
	}
	data := snapshotWriteBackend(t, backend)
	if data.Effects != 1 || len(data.Receipts) != 1 {
		t.Fatal("duplicate side effect")
	}
}

func TestPartialSuccessStopsAtExternalDriftWithoutUndoingEarlierWrite(t *testing.T) {
	backend := &memoryWriteBackend{data: initialWriteDatabase()}
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	plan := writePlan()
	first := executeVerified(t, writer, prepareWrite(t, plan, 0, writer, nil))
	second := prepareWrite(t, plan, 1, writer, []resourcewrite.Completed{first})
	_ = backend.transaction(context.Background(), func(d *writeDatabase) error {
		d.Config.Version = "external-version"
		d.Config.Items[0].Version = "external-item"
		d.Config.Items[0].Value.Text = "45"
		return nil
	})
	receipt, err := writer.Apply(context.Background(), second)
	if err != nil || receipt.Status != "REJECTED" || receipt.Code != "precondition_failed" {
		t.Fatal("external change overwritten")
	}
	v, err := writer.Verify(context.Background(), first.Intent, first.Receipt)
	if err != nil || v.State != "DRIFTED" {
		t.Fatal("stale success presented as current")
	}
	if _, err := resourcewrite.Prepare(plan, 2, "third-operation", writerFor(t, platform, "experiment").Describe(), time.Now().Add(time.Hour), []resourcewrite.Completed{first, {Intent: second, Receipt: receipt}}); err == nil {
		t.Fatal("continued past failed dependency")
	}
	data := snapshotWriteBackend(t, backend)
	if data.Effects != 1 || data.Config.Items[0].Value.Text != "45" || len(data.Experiments.Items) != 0 {
		t.Fatal("partial success rolled back or remaining action ran")
	}
}

func TestMissingReceiptIsUnknownAndExpiredIntentsNeverStartNewWrites(t *testing.T) {
	backend := &memoryWriteBackend{data: initialWriteDatabase()}
	platform := newWritePlatform(t, backend)
	writer := writerFor(t, platform, "config")
	intent := prepareWrite(t, writePlan(), 0, writer, nil)
	if _, err := writer.Lookup(context.Background(), intent); !errors.Is(err, resourcewrite.ErrUnknown) {
		t.Fatal("404 incorrectly proves non-execution")
	}
	intent.ExpiresAt = time.Now().Add(-time.Second)
	intent.Hash = intent.Digest()
	if _, err := writer.Apply(context.Background(), intent); !errors.Is(err, resourcewrite.ErrExpired) || platform.calls.Load() != 1 {
		t.Fatal("expired intent reached provider")
	}
	// The provider also rejects expiry after network transmission, before its decision.
	intent.ExpiresAt = time.Now().Add(200 * time.Millisecond)
	intent.Hash = intent.Digest()
	platform.entered = make(chan struct{}, 1)
	platform.release = make(chan struct{})
	result := make(chan resourcewrite.Receipt, 1)
	fail := make(chan error, 1)
	go func() { r, err := writer.Apply(context.Background(), intent); result <- r; fail <- err }()
	select {
	case <-platform.entered:
	case <-time.After(2 * time.Second):
		close(platform.release)
		t.Fatal("request did not reach provider")
	}
	time.Sleep(time.Until(intent.ExpiresAt) + 10*time.Millisecond)
	close(platform.release)
	receipt := <-result
	if err := <-fail; err != nil || receipt.Status != "REJECTED" || receipt.Code != "intent_expired" {
		t.Fatal("late request was not rejected")
	}
	if snapshotWriteBackend(t, backend).Effects != 0 {
		t.Fatal("expired operation changed resource")
	}
}

func TestReferenceWriterRejectsUntrustedScopeAndTransportBeforeRequests(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	base := reference.WriteConnection{Connection: reference.Connection{ID: "config-ref", Version: "1", BaseURL: server.URL, Token: "writer-token"}, Targets: []planning.Target{configTarget}}
	for _, mode := range []string{"no-target", "no-token", "wrong-connection", "http", "userinfo", "query", "fragment", "insecure"} {
		t.Run(mode, func(t *testing.T) {
			c := base
			client := server.Client()
			switch mode {
			case "no-target":
				c.Targets = nil
			case "no-token":
				c.Token = ""
			case "wrong-connection":
				c.ID = "another"
			case "http":
				c.BaseURL = "http://127.0.0.1"
			case "userinfo":
				c.BaseURL = "https://user:pass@example.invalid"
			case "query":
				c.BaseURL += "?"
			case "fragment":
				c.BaseURL += "#x"
			case "insecure":
				client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			}
			if _, err := reference.NewConfigWriter(c, client); err == nil {
				t.Fatal("unsafe writer deployment accepted")
			}
		})
	}
	w, err := reference.NewConfigWriter(base, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	intent := prepareWrite(t, writePlan(), 0, w, nil)
	for _, mode := range []string{"tenant", "environment", "resource", "connector", "descriptor", "activation", "hash"} {
		t.Run(mode, func(t *testing.T) {
			i := planning.Clone(intent)
			switch mode {
			case "tenant":
				i.Target.TenantID = "other"
			case "environment":
				i.Target.Environment = "prod"
			case "resource":
				i.Target.ResourceRef = "other"
			case "connector":
				i.Target.ConnectorRef = "other"
			case "descriptor":
				i.Writer.ConnectionVersion = "2"
			case "activation":
				i.Operation = "experiment.activate"
			}
			i.Hash = i.Digest()
			if mode == "hash" {
				i.Hash = "bad"
			}
			if _, err := w.Apply(context.Background(), i); !errors.Is(err, resourcewrite.ErrInvalid) {
				t.Fatal("untrusted request accepted")
			}
		})
	}
	if calls != 0 {
		t.Fatal("invalid operation contacted provider")
	}
}

func TestReferenceWriterRejectsAmbiguousResponsesWithoutRetriesOrLeaks(t *testing.T) {
	for _, mode := range []string{"wrong-target", "wrong-hash", "duplicate", "null", "unknown-field", "redirect", "server-error", "oversize", "bad-version", "unproven-conflict"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var i resourcewrite.Intent
				_ = json.NewDecoder(r.Body).Decode(&i)
				receipt := resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", ID: "receipt", OperationID: i.OperationID, IntentHash: i.Hash, Kind: i.Kind, Target: i.Target, Key: i.Key, Status: "APPLIED", BeforeResourceVersion: i.ResourceVersion, ResourceVersion: "resource-2", ItemVersion: "item-2", ValueHash: i.ValueDigest(), Effect: i.Effect, RecordedAt: time.Now().UTC()}
				switch mode {
				case "wrong-target":
					receipt.Target.TenantID = "other"
				case "wrong-hash":
					receipt.IntentHash = strings.Repeat("c", 64)
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(307)
					return
				case "server-error":
					w.WriteHeader(503)
					_, _ = w.Write([]byte("private-provider-response"))
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
					return
				case "bad-version":
					receipt.ResourceVersion = i.ResourceVersion
				case "unproven-conflict":
					w.WriteHeader(409)
					_, _ = w.Write([]byte("private-provider-response"))
					return
				}
				data, _ := json.Marshal(receipt)
				body := string(data)
				switch mode {
				case "duplicate":
					body = strings.Replace(body, `"status":"APPLIED"`, `"status":"APPLIED","status":"APPLIED"`, 1)
				case "null":
					body = strings.Replace(body, `"effect":"publish_required"`, `"effect":null`, 1)
				case "unknown-field":
					body = strings.Replace(body, `"status":"APPLIED"`, `"private":"private-provider-response","status":"APPLIED"`, 1)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			writer, err := reference.NewConfigWriter(reference.WriteConnection{Connection: reference.Connection{ID: "config-ref", Version: "1", BaseURL: server.URL, Token: "writer-token"}, Targets: []planning.Target{configTarget}}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			i := prepareWrite(t, writePlan(), 0, writer, nil)
			receipt, err := writer.Apply(context.Background(), i)
			if !errors.Is(err, resourcewrite.ErrUnknown) || receipt.ID != "" || strings.Contains(err.Error(), "private") || calls != 1 {
				t.Fatalf("unsafe result %v %+v calls=%d", err, receipt, calls)
			}
		})
	}
}
