package apiapp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/servicecatalog"
)

func createWriteBundle(t *testing.T, address string) delivery.Bundle {
	t.Helper()
	apiNetworkWrite(t, address, "POST", "/api/v1/changes", `{"idempotencyKey":"create","title":"Write acceptance","environment":"test","services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"freeze","expectedVersion":1,"services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	var plan struct {
		Plan struct {
			Hash  string `json:"hash"`
			State string `json:"state"`
		} `json:"plan"`
	}
	data := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", `{"idempotencyKey":"plan","expectedVersion":4,"snapshotId":"snapshot-1","requirements":[{"serviceId":"S1","config":[{"key":"feature.timeout","desired":{"type":"integer","text":"200"},"reason":"fixture requirement"},{"key":"feature.extra","desired":{"type":"string","text":"write-business-value"},"reason":"fixture requirement"}],"experiments":[{"key":"checkout-test","desired":{"variants":[{"name":"control","weight":50},{"name":"treatment","weight":50}],"audience":"test-users","metrics":["conversion"],"allocation":"weighted"},"reason":"fixture draft"}]}]}`, 201)
	if json.Unmarshal(data, &plan) != nil || plan.Plan.State != "READY_FOR_REVIEW" {
		t.Fatalf("write plan not ready: %s", data)
	}
	body, _ := json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedPlanHash": plan.Plan.Hash})
	var b delivery.Bundle
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans/plan-1/archive", string(body), 201), &b) != nil {
		t.Fatal("archive")
	}
	return b
}
func approveAndStartWrite(t *testing.T, address string, b delivery.Bundle) (delivery.WriteApproval, delivery.WriteExecution, string) {
	t.Helper()
	root := "/api/v1/deliveries/" + b.ID
	body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedDigest": b.Digest, "ttlSeconds": 3600, "reason": "reviewed actual config and AB draft", "findingsReviewed": true})
	var a delivery.WriteApproval
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", root+"/write-approvals", string(body), 201), &a) != nil {
		t.Fatal("approval")
	}
	body, _ = json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedDigest": b.Digest, "approvalId": a.ID})
	var run delivery.WriteExecution
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", root+"/write-executions", string(body), 202), &run) != nil {
		t.Fatal("execution")
	}
	return a, run, string(body)
}
func buildWriteWorker(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "writeworker")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/writeworker")
	cmd.Dir = "../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build write worker: %v %s", err, output)
	}
	return binary
}
func startWriteWorkerProcess(t *testing.T, binary string) *simulationWorker {
	t.Helper()
	p := &simulationWorker{cmd: exec.Command(binary), done: make(chan error, 1), logPath: filepath.Join(t.TempDir(), "write-worker.log")}
	p.cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir()}
	for _, name := range []string{"AUTH_FILE", "SERVICE_CATALOG_FILE", "PLANNING_CONNECTORS_FILE", "ARCHIVE_DATABASE_FILE", "WRITES_FILE", "WORKSPACE_ROOT", "GITLAB_BASE_URL", "GITLAB_TOKEN", "GITLAB_CA_FILE"} {
		p.cmd.Env = append(p.cmd.Env, "AGENT_PLATFORM_"+name+"="+os.Getenv("AGENT_PLATFORM_"+name))
	}
	file, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout, p.cmd.Stderr = file, file
	if err := p.cmd.Start(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); _ = file.Close() }()
	t.Cleanup(func() { p.stop(t, false) })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(readLocalFile(t, p.logPath)), "delivery write worker ready") {
			return p
		}
		select {
		case <-p.done:
			p.stopped = true
			t.Fatalf("worker: %s", readLocalFile(t, p.logPath))
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("write worker startup timed out")
	return p
}
func pollWrite(t *testing.T, address string, b delivery.Bundle, id string, want string) delivery.WriteExecution {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var run delivery.WriteExecution
	for time.Now().Before(deadline) {
		data := apiNetworkWrite(t, address, "GET", "/api/v1/deliveries/"+b.ID+"/write-executions/"+id, "", 200)
		if json.Unmarshal(data, &run) != nil {
			t.Fatal("invalid execution")
		}
		if run.State == want {
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("write timed out: %s %s", run.State, run.ErrorCode)
	return run
}
func requireWriteTemporal(t *testing.T) {
	t.Helper()
	archiveTestFile(t)
	if os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS") == "" {
		t.Skip("set PostgreSQL and Temporal fixtures for write API integration")
	}
}

func TestWriteAPIExecutesConfigAndInactiveExperimentThroughDedicatedWorker(t *testing.T) {
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	root := "/api/v1/deliveries/" + b.ID
	apiNetworkWrite(t, process.address, "GET", root+"/write-access", "", 200)
	_, run, body := approveAndStartWrite(t, process.address, b)
	if platform.posts.Load() != 0 {
		t.Fatal("API performed external write")
	}
	apiNetworkWrite(t, process.address, "POST", root+"/write-executions", body, 200)
	worker := startWriteWorkerProcess(t, buildWriteWorker(t))
	completed := pollWrite(t, process.address, b, run.ID, "SUCCEEDED")
	if len(completed.Operations) != 3 || platform.effects.Load() != 3 || platform.posts.Load() != 3 {
		t.Fatal("unexpected effects")
	}
	for _, op := range completed.Operations {
		if op.Intent != nil || op.IntentHash == "" || op.Receipt == nil || op.Verification == nil {
			t.Fatal("missing evidence or exposed internal intent")
		}
	}
	platform.mu.Lock()
	draft := platform.experiments.Items[0].State
	platform.mu.Unlock()
	if draft != "DRAFT" {
		t.Fatal("experiment activated")
	}
	worker.stop(t, false)
	process.stop(t)
	restarted := startAPI(t, buildAPI(t))
	apiNetworkWrite(t, restarted.address, "POST", root+"/write-executions", body, 200)
	if platform.posts.Load() != 3 || fixture.modelCalls.Load() != 1 {
		t.Fatal("replay invoked external write/model")
	}
}

func TestWriteAPICurrentAuthorizationCodeAndRoutingChangesStopNewEffects(t *testing.T) {
	requireWriteTemporal(t)
	for _, mode := range []string{"permission", "code", "connection", "self-approval", "routing", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			platform := installWriteFixture(t, fixture)
			process := startAPI(t, buildAPI(t))
			b := createWriteBundle(t, process.address)
			a, run, _ := approveAndStartWrite(t, process.address, b)
			// Worker captures deployment routing before the file changes.
			service, _, err := apiapp.WriteWorkerFromEnvironment(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer service.Store.Close()
			switch mode {
			case "permission":
				changeWriteGrants(t, func(ops []string) []string {
					out := []string{}
					for _, op := range ops {
						if op != "execute_write" {
							out = append(out, op)
						}
					}
					return out
				})
			case "code":
				if os.WriteFile(filepath.Join(fixture.directory, "source-drift"), []byte("drift"), 0o600) != nil {
					t.Fatal("fixture")
				}
			case "connection":
				platform.settings.Connectors[0].Version = "2"
				writePrivateJSON(t, platform.filename, platform.settings)
			case "self-approval":
				platform.settings.AllowSelfApproval = false
				writePrivateJSON(t, platform.filename, platform.settings)
			case "routing":
				platform.settings.TaskQueue = "different-queue"
				writePrivateJSON(t, platform.filename, platform.settings)
			case "revoke":
				body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedVersion": a.Version + 1, "reason": "stop before dispatch"})
				apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+b.ID+"/write-approvals/"+a.ID+"/revoke", string(body), 200)
			}
			ref := delivery.Ref{TenantID: b.TenantID, RunID: run.ID, OperationID: run.Operations[0].ID}
			if next, err := service.Step(context.Background(), ref); err != nil || next {
				t.Fatalf("changed policy continued: %v", err)
			}
			if state, err := service.Finish(context.Background(), ref); err != nil || state != "BLOCKED" {
				t.Fatal("not blocked")
			}
			if platform.posts.Load() != 0 {
				t.Fatal("changed input reached platform")
			}
		})
	}
}

func changeWriteGrants(t *testing.T, fn func([]string) []string) {
	t.Helper()
	path := os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")
	var config servicecatalog.Config
	if json.Unmarshal(readLocalFile(t, path), &config) != nil {
		t.Fatal("catalog")
	}
	for n := range config.Grants {
		config.Grants[n].Operations = fn(config.Grants[n].Operations)
	}
	writePrivateJSON(t, path, config)
}
