package delivery_test

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/connector/mcpbridge/mcptest"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/orchestration"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/client"
)

type companyPolicy struct {
	adapter   *mcpbridge.Adapter
	denyRead  atomic.Bool
	denyWrite atomic.Bool
}

func (p *companyPolicy) Authorize(_ context.Context, subject string, _ delivery.Bundle, op string) (string, error) {
	if op == "read" && p.denyRead.Load() || op != "read" && p.denyWrite.Load() {
		return "", delivery.ErrPolicy
	}
	return subject + ":" + op, nil
}
func (p *companyPolicy) Writer(_ context.Context, _ string, _ planning.Target) (resourcewrite.Writer, error) {
	return p.adapter, nil
}
func (p *companyPolicy) Revalidate(ctx context.Context, b delivery.Bundle, prefix []resourcewrite.Completed) error {
	reader := mcpbridge.Reader{Adapter: p.adapter}
	planner, err := planning.NewPlanner(map[string]planning.ConfigReader{p.adapter.Targets[0].ConnectorRef: reader}, nil, planning.NewDiffAnalyzer())
	if err != nil {
		return err
	}
	return resourcewrite.RevalidateRemaining(ctx, planner, b.Plan, prefix)
}

type companyFixture struct {
	service  *delivery.WriteService
	policy   *companyPolicy
	provider *mcptest.Provider
	run      delivery.WriteExecution
	bundle   delivery.Bundle
	approval delivery.WriteApproval
	dsn      string
}

func newCompanyFixture(t *testing.T, combined bool) *companyFixture {
	t.Helper()
	ctx := context.Background()
	dsn := databaseURL(t)
	store, err := delivery.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	request, b := readySimulationFixture("company-" + rand.Text())
	b.Plan.Actions = b.Plan.Actions[:1]
	target := b.Plan.Actions[0].Target
	p := mcptest.New("company_"+rand.Text()+"_", planning.ConfigState{Target: target, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{}}, combined)
	server := httptest.NewServer(p.Handler())
	t.Cleanup(server.Close)
	c := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "streamable-http", URL: server.URL}}
	catalog, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := &mcpbridge.Adapter{ReadClient: c, WriteClient: c, Binding: p.Binding(catalog), Version: "v1", Targets: []planning.Target{target}}
	for n := range b.Plan.Actions {
		action := &b.Plan.Actions[n]
		action.Descriptor = (mcpbridge.Reader{Adapter: a}).Describe()
		action.Effect = "publish_required"
		action.Hash = ""
		action.Hash = planning.Hash(*action)
	}
	b.Plan.Observations = []planning.Observation{{Kind: "config", Target: target, Descriptor: b.Plan.Actions[0].Descriptor, Version: "v1"}}
	b.Plan.Hash = ""
	b.Plan.Hash = planning.Hash(b.Plan)
	request.PlanHash = b.Plan.Hash
	b, _, err = store.Save(ctx, request, b)
	if err != nil {
		t.Fatal(err)
	}
	policy := &companyPolicy{adapter: a}
	service := &delivery.WriteService{Store: store, Policy: policy}
	approval, _, err := service.Approve(ctx, delivery.ApprovalInput{TenantID: b.TenantID, BundleID: b.ID, Subject: "bob", ExpectedDigest: b.Digest, IdempotencyKey: "approve", TTLSeconds: 3600, Reason: "save and submit; company approves separately", FindingsReviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := service.Start(ctx, delivery.WriteStartInput{TenantID: b.TenantID, BundleID: b.ID, Subject: "alice", IdempotencyKey: "start", ExpectedDigest: b.Digest, ApprovalID: approval.ID})
	if err != nil || !run.CompanyFlow {
		t.Fatal("company start", err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Error("cleanup connection")
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, `DELETE FROM agent_delivery.write_outbox WHERE tenant_id=$1`, b.TenantID); err != nil {
			t.Error("cleanup outbox")
		}
	})
	return &companyFixture{service: service, policy: policy, provider: p, run: run, bundle: b, approval: approval, dsn: dsn}
}
func (f *companyFixture) ref() delivery.Ref {
	return delivery.Ref{TenantID: f.run.TenantID, RunID: f.run.ID, OperationID: f.run.Operations[0].ID}
}
func (f *companyFixture) save(t *testing.T) {
	t.Helper()
	ok, err := f.service.Step(context.Background(), f.ref())
	if err != nil || !ok {
		t.Fatal("save", err)
	}
}
func (f *companyFixture) submit(t *testing.T) {
	t.Helper()
	ok, err := f.service.CompanySubmit(context.Background(), f.ref())
	if err != nil || !ok {
		t.Fatal("submit", err)
	}
}
func TestCompanyLedgerSeparatesApprovalPublicationAndRestarts(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "combined"}[combined], func(t *testing.T) {
			f := newCompanyFixture(t, combined)
			ctx := context.Background()
			f.save(t)
			f.submit(t)
			if done, err := f.service.CompanyObserve(ctx, f.ref()); err != nil || done {
				t.Fatal("premature completion", err)
			}
			if state, err := f.service.Finish(ctx, f.ref()); err != nil || state != "WAITING_COMPANY" {
				t.Fatal(state, err)
			}
			// Permission expiry/revocation stops new writes but does not authorize or
			// prevent read-only observation of the company's own later publication.
			f.policy.denyWrite.Store(true)
			f.service.Store.Close()
			store, err := delivery.Open(ctx, f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			f.service.Store = store
			f.provider.Configure("APPROVED", "PENDING", false, false)
			done, err := f.service.CompanyObserve(ctx, f.ref())
			if err != nil || done {
				t.Fatal("approval treated as publication")
			}
			f.policy.denyRead.Store(true)
			_, _, before := f.provider.Counts()
			_, _ = f.service.CompanyObserve(ctx, f.ref())
			_, _, after := f.provider.Counts()
			if before != after {
				t.Fatal("queried after read revocation")
			}
			f.policy.denyRead.Store(false)
			f.provider.Configure("APPROVED", "PUBLISHED", false, false)
			done, err = f.service.CompanyObserve(ctx, f.ref())
			if err != nil || !done {
				t.Fatal("publication", err)
			}
			if state, err := f.service.Finish(ctx, f.ref()); err != nil || state != "SUCCEEDED" {
				t.Fatal(state, err)
			}
			saves, submits, _ := f.provider.Counts()
			want := 1
			if combined {
				want = 0
			}
			if saves != 1 || submits != want {
				t.Fatalf("duplicate effects %d/%d", saves, submits)
			}
		})
	}
}
func TestCompanyLostSubmissionAndRevokedUnsentSubmission(t *testing.T) {
	for _, mode := range []string{"lost-submit", "revoked", "wrong-change", "rejected", "drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompanyFixture(t, false)
			ctx := context.Background()
			f.save(t)
			if mode == "revoked" {
				f.policy.denyWrite.Store(true)
				ok, err := f.service.CompanySubmit(ctx, f.ref())
				if err != nil || ok {
					t.Fatal("submitted revoked grant")
				}
				_, submits, _ := f.provider.Counts()
				if submits != 0 {
					t.Fatal("unauthorized submission")
				}
				return
			}
			if mode == "lost-submit" {
				f.provider.LoseResponse(false, true)
				ok, err := f.service.CompanySubmit(ctx, f.ref())
				if err != nil || ok {
					t.Fatal("lost result")
				}
				if _, err := f.service.CompanyObserve(ctx, f.ref()); err != nil {
					t.Fatal(err)
				}
				_, submits, _ := f.provider.Counts()
				if submits != 1 {
					t.Fatal("resent ticket")
				}
				return
			}
			f.submit(t)
			switch mode {
			case "wrong-change":
				f.provider.Configure("APPROVED", "PUBLISHED", true, false)
			case "rejected":
				f.provider.Configure("REJECTED", "PUBLISHED", false, false)
			default:
				f.provider.Configure("APPROVED", "PUBLISHED", false, true)
			}
			done, err := f.service.CompanyObserve(ctx, f.ref())
			if err != nil {
				t.Fatal(err)
			}
			if done != (mode != "wrong-change") {
				t.Fatal("bad evidence completed", mode)
			}
			state, err := f.service.Finish(ctx, f.ref())
			if err != nil || state == "SUCCEEDED" {
				t.Fatal("false success", state, err)
			}
		})
	}
}
func TestCompanyConcurrentSubmissionDispatchesOnce(t *testing.T) {
	f := newCompanyFixture(t, false)
	f.save(t)
	var group sync.WaitGroup
	for range 6 {
		group.Go(func() { _, _ = f.service.CompanySubmit(context.Background(), f.ref()) })
	}
	group.Wait()
	_, submits, _ := f.provider.Counts()
	if submits != 1 {
		t.Fatalf("concurrent submissions %d", submits)
	}
}
func TestCompanyTemporalResumesApprovalWaitWithoutRepeatingWrites(t *testing.T) {
	address := os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("requires real Temporal")
	}
	f := newCompanyFixture(t, false)
	queue := "company-test-" + rand.Text()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := func() func() {
		workerCtx, stop := context.WithCancel(context.Background())
		done := make(chan error, 1)
		ready := make(chan struct{})
		go func() {
			done <- orchestration.RunWrites(workerCtx, f.service, address, "agent-platform-d4", queue, rand.Text(), func() { close(ready) })
		}()
		select {
		case <-ready:
		case err := <-done:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("worker startup")
		}
		var once sync.Once
		cleanup := func() {
			once.Do(func() {
				stop()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(15 * time.Second):
					t.Error("worker stop")
				}
			})
		}
		t.Cleanup(cleanup)
		return cleanup
	}
	stop := start()
	for {
		r, err := f.service.Store.GetWriteExecution(ctx, f.ref())
		if err != nil {
			t.Fatal(err)
		}
		if r.State == "WAITING_COMPANY" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("not waiting")
		case <-time.After(30 * time.Millisecond):
		}
	}
	stop()
	f.provider.Configure("APPROVED", "PUBLISHED", false, false)
	stopAgain := start()
	defer stopAgain()
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: "agent-platform-d4"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var result string
	if err := c.GetWorkflow(ctx, f.run.WorkflowID, "").Get(ctx, &result); err != nil || result != "SUCCEEDED" {
		t.Fatal("workflow result", result, err)
	}
	saves, submits, _ := f.provider.Counts()
	if saves != 1 || submits != 1 {
		t.Fatal("restart repeated effects")
	}
	assertWriteHistory(t, ctx, c, f.run)
}
