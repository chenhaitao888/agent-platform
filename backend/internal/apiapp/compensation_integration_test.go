package apiapp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/delivery"
)

func allowCompensation(t *testing.T) {
	t.Helper()
	changeWriteGrants(t, func(ops []string) []string {
		return append(ops, "prepare_compensation", "approve_compensation", "execute_compensation")
	})
}
func prepareCompensationHTTP(t *testing.T, address string, b delivery.Bundle, run delivery.WriteExecution) (delivery.CompensationPlan, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedExecutionHash": run.EvidenceHash, "reason": "restore only supported configuration updates"})
	var p delivery.CompensationPlan
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", "/api/v1/deliveries/"+b.ID+"/write-executions/"+run.ID+"/compensations", string(body), 201), &p) != nil {
		t.Fatal("invalid compensation plan")
	}
	return p, string(body)
}
func readCompensationBundle(t *testing.T, address string, p delivery.CompensationPlan) delivery.Bundle {
	t.Helper()
	var b delivery.Bundle
	if json.Unmarshal(apiNetworkWrite(t, address, "GET", "/api/v1/deliveries/"+p.DerivedBundleID, "", 200), &b) != nil || b.Compensation == nil {
		t.Fatal("missing compensation bundle")
	}
	return b
}
func finishOriginalWithoutWorker(t *testing.T, b delivery.Bundle, run delivery.WriteExecution) delivery.WriteExecution {
	t.Helper()
	service, _, err := apiapp.WriteWorkerFromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Store.Close()
	ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID}
	for _, o := range run.Operations {
		ref.OperationID = o.ID
		if next, err := service.Step(context.Background(), ref); err != nil || !next {
			t.Fatalf("original step: %v", err)
		}
	}
	if state, err := service.Finish(context.Background(), ref); err != nil || state != "SUCCEEDED" {
		t.Fatal("original did not complete")
	}
	r, err := service.Store.GetWriteExecution(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return r.Public()
}

func TestCompensationAPIRequiresSeparateRolesAndPreservesManualItems(t *testing.T) {
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	a, run, _ := approveAndStartWrite(t, process.address, b)
	run = finishOriginalWithoutWorker(t, b, run)
	root := "/api/v1/deliveries/" + b.ID
	preparePath := root + "/write-executions/" + run.ID + "/compensations"
	body, _ := json.Marshal(map[string]string{"idempotencyKey": "denied", "expectedExecutionHash": run.EvidenceHash, "reason": "review"})
	apiNetworkWrite(t, process.address, "POST", preparePath, string(body), 403)
	allowCompensation(t)
	for _, field := range []string{`"value":"injected"`, `"subject":"another"`, `"plan":{}`, `"completed":[]`} {
		apiNetworkWrite(t, process.address, "POST", preparePath, `{"idempotencyKey":"bad","expectedExecutionHash":"`+run.EvidenceHash+`","reason":"test",`+field+`}`, 400)
	}
	p, bodyText := prepareCompensationHTTP(t, process.address, b, run)
	if p.State != "READY_FOR_REVIEW" || len(p.Plan.Actions) != 1 || platform.posts.Load() != 3 {
		t.Fatal("preparation wrote or selected unsupported actions")
	}
	reverse := readCompensationBundle(t, process.address, p)
	if reverse.Compensation.ManualItems != 2 || reverse.Compensation.ExecutionHash != run.EvidenceHash {
		t.Fatal("manual items or source association missing")
	}
	apiNetworkWrite(t, process.address, "POST", preparePath, bodyText, 200)
	apiNetworkWrite(t, process.address, "GET", root+"/compensations/"+p.ID, "", 200)
	apiNetworkWrite(t, process.address, "GET", "/api/v1/deliveries/"+reverse.ID+"/compensations/"+p.ID, "", 404)
	oldApproval, _ := json.Marshal(map[string]string{"idempotencyKey": "old-grant", "expectedDigest": reverse.Digest, "approvalId": a.ID})
	apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+reverse.ID+"/write-executions", string(oldApproval), 409)
	changeWriteGrants(t, func(ops []string) []string {
		out := []string{}
		for _, op := range ops {
			if op != "approve_compensation" {
				out = append(out, op)
			}
		}
		return out
	})
	denied, _ := json.Marshal(map[string]any{"idempotencyKey": "forward-role-only", "expectedDigest": reverse.Digest, "ttlSeconds": 900, "reason": "cannot reuse forward permission", "findingsReviewed": true})
	apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+reverse.ID+"/write-approvals", string(denied), 403)
	changeWriteGrants(t, func(ops []string) []string { return append(ops, "approve_compensation") })
	_, compRun, _ := approveAndStartWrite(t, process.address, reverse)
	worker := startWriteWorkerProcess(t, buildWriteWorker(t))
	pollWrite(t, process.address, reverse, compRun.ID, "SUCCEEDED")
	worker.stop(t, false)
	platform.mu.Lock()
	value := ""
	for _, item := range platform.config.Items {
		if item.Key == "feature.timeout" {
			value = item.Value.Text
		}
	}
	draft := platform.experiments.Items[0].State
	platform.mu.Unlock()
	if value != "100" || draft != "DRAFT" || platform.posts.Load() != 4 {
		t.Fatal("compensation changed unsupported resources or repeated a write")
	}
	after := pollWrite(t, process.address, b, run.ID, "SUCCEEDED")
	if after.EvidenceHash != run.EvidenceHash {
		t.Fatal("original execution altered")
	}
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("compensation invoked model")
	}
}

func TestCompensationPreparedPlanStopsOnNewDriftOrRevocation(t *testing.T) {
	archiveTestFile(t)
	for _, mode := range []string{"resource", "code", "permission", "revoke", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			platform := installWriteFixture(t, fixture)
			allowCompensation(t)
			process := startAPI(t, buildAPI(t))
			b := createWriteBundle(t, process.address)
			_, run, _ := approveAndStartWrite(t, process.address, b)
			run = finishOriginalWithoutWorker(t, b, run)
			p, _ := prepareCompensationHTTP(t, process.address, b, run)
			reverse := readCompensationBundle(t, process.address, p)
			a, compRun, _ := approveAndStartWrite(t, process.address, reverse)
			switch mode {
			case "resource":
				platform.mu.Lock()
				platform.config.Version = "external-change"
				platform.config.Items[0].Value.Text = "777"
				platform.mu.Unlock()
			case "code":
				if os.WriteFile(filepath.Join(fixture.directory, "source-drift"), []byte("changed"), 0o600) != nil {
					t.Fatal("fixture")
				}
			case "permission":
				changeWriteGrants(t, func(ops []string) []string {
					out := []string{}
					for _, op := range ops {
						if op != "execute_compensation" {
							out = append(out, op)
						}
					}
					return out
				})
			case "revoke":
				body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedVersion": a.Version + 1, "reason": "stop restoration"})
				apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+reverse.ID+"/write-approvals/"+a.ID+"/revoke", string(body), 200)
			case "lost-response":
				platform.drop.Store(true)
			}
			service, _, err := apiapp.WriteWorkerFromEnvironment(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer service.Store.Close()
			ref := delivery.Ref{TenantID: reverse.TenantID, RunID: compRun.ID, OperationID: compRun.Operations[0].ID}
			if next, err := service.Step(context.Background(), ref); err != nil || next {
				t.Fatalf("unsafe continuation: %v", err)
			}
			state, err := service.Finish(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "lost-response" {
				if state != "UNKNOWN" {
					t.Fatal("lost outcome not retained")
				}
				if state, err := service.Reconcile(context.Background(), ref); err != nil || state != "SUCCEEDED" {
					t.Fatal("compensation outcome not recovered")
				}
				if platform.posts.Load() != 4 {
					t.Fatal("compensation resent")
				}
			} else if state != "BLOCKED" || platform.posts.Load() != 3 {
				t.Fatal("changed conditions allowed compensation")
			}
		})
	}
}

func TestCompensationRestoresSupportedEffectsAfterPartialFailure(t *testing.T) {
	archiveTestFile(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	allowCompensation(t)
	platform.rejectExperiment.Store(true)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	_, run, _ := approveAndStartWrite(t, process.address, b)
	service, _, err := apiapp.WriteWorkerFromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Store.Close()
	ctx := context.Background()
	ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID}
	for _, o := range run.Operations {
		ref.OperationID = o.ID
		next, err := service.Step(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if !next {
			break
		}
	}
	if state, err := service.Finish(ctx, ref); err != nil || state != "FAILED" {
		t.Fatal("partial failure not recorded")
	}
	failed, err := service.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := prepareCompensationHTTP(t, process.address, b, failed.Public())
	reverse := readCompensationBundle(t, process.address, p)
	if len(p.Plan.Actions) != 1 || reverse.Compensation.ManualItems != 1 {
		t.Fatal("partial disposition incorrect")
	}
	_, compRun, _ := approveAndStartWrite(t, process.address, reverse)
	for _, o := range compRun.Operations {
		if next, err := service.Step(ctx, delivery.Ref{TenantID: b.TenantID, RunID: compRun.ID, OperationID: o.ID}); err != nil || !next {
			t.Fatal("partial compensation failed")
		}
	}
	if state, err := service.Finish(ctx, delivery.Ref{TenantID: b.TenantID, RunID: compRun.ID}); err != nil || state != "SUCCEEDED" {
		t.Fatal("compensation incomplete")
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	timeout := ""
	for _, item := range platform.config.Items {
		if item.Key == "feature.timeout" {
			timeout = item.Value.Text
		}
	}
	if timeout != "100" || len(platform.config.Items) != 2 || len(platform.experiments.Items) != 0 || platform.effects.Load() != 3 {
		t.Fatal("manual item removed or rejected effect invented")
	}
}
