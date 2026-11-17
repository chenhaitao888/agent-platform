package apiapp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/servicecatalog"
	"github.com/jackc/pgx/v5"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

func configureSimulationFixture(t *testing.T, fixture deploymentFixture) apiapp.SimulationSettings {
	t.Helper()
	address := os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("set AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS for Temporal integration")
	}
	configureArchiveFixture(t, archiveTestFile(t))
	settings := apiapp.SimulationSettings{TemporalAddress: address, Namespace: "agent-platform-d4", TaskQueue: "delivery-test-" + rand.Text(), AllowSelfApproval: true, LeaseSeconds: 2}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(fixture.directory, "simulation.json")
	if os.WriteFile(filename, data, 0o600) != nil {
		t.Fatal("write simulation fixture")
	}
	t.Setenv("AGENT_PLATFORM_SIMULATION_FILE", filename)
	var catalog servicecatalog.Config
	if json.Unmarshal(readLocalFile(t, os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")), &catalog) != nil {
		t.Fatal("invalid fixture catalog")
	}
	for i := range catalog.Grants {
		catalog.Grants[i].Operations = append(catalog.Grants[i].Operations, "approve_simulation", "simulate")
	}
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	installCatalog(t, string(data))
	return settings
}
func createSimulationBundle(t *testing.T, address string) delivery.Bundle {
	t.Helper()
	apiNetworkWrite(t, address, "POST", "/api/v1/changes", `{"idempotencyKey":"create","title":"Simulation acceptance","environment":"test","services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"freeze","expectedVersion":1,"services":[{"serviceId":"S1","sourceBranch":"feature"}]}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	var result struct {
		Plan struct {
			Hash  string `json:"hash"`
			State string `json:"state"`
		} `json:"plan"`
	}
	data := apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans", `{"idempotencyKey":"plan","expectedVersion":4,"snapshotId":"snapshot-1","requirements":[{"serviceId":"S1","config":[{"key":"feature.timeout","desired":{"type":"integer","text":"200"},"reason":"fixture requirement"},{"key":"feature.extra","desired":{"type":"string","text":"business-value-not-for-temporal-history"},"reason":"fixture requirement"}],"experimentNotInvolved":"no experiment changes"}]}`, 201)
	if json.Unmarshal(data, &result) != nil || result.Plan.State != "READY_FOR_REVIEW" {
		t.Fatalf("plan not ready: %s", data)
	}
	body, _ := json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedPlanHash": result.Plan.Hash})
	var bundle delivery.Bundle
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", "/api/v1/changes/change-1/plans/plan-1/archive", string(body), 201), &bundle) != nil {
		t.Fatal("invalid archive")
	}
	return bundle
}
func approveSimulationFixture(t *testing.T, address string, b delivery.Bundle, scenario string) (delivery.Approval, delivery.Simulation, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedDigest": b.Digest, "ttlSeconds": 3600, "reason": "reviewed fixture Findings; simulation only", "findingsReviewed": true})
	var approval delivery.Approval
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", "/api/v1/deliveries/"+b.ID+"/simulation-approvals", string(body), 201), &approval) != nil {
		t.Fatal("invalid approval")
	}
	body, _ = json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedDigest": b.Digest, "approvalId": approval.ID, "scenario": scenario})
	var run delivery.Simulation
	if json.Unmarshal(apiNetworkWrite(t, address, "POST", "/api/v1/deliveries/"+b.ID+"/simulations", string(body), 202), &run) != nil {
		t.Fatal("invalid simulation")
	}
	return approval, run, string(body)
}
func buildSimulationWorker(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "deliveryworker")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/deliveryworker")
	cmd.Dir = "../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v %s", err, output)
	}
	return binary
}

type simulationWorker struct {
	cmd     *exec.Cmd
	done    chan error
	logPath string
	stopped bool
}

func startSimulationWorker(t *testing.T, binary string) *simulationWorker {
	t.Helper()
	p := &simulationWorker{cmd: exec.Command(binary), done: make(chan error, 1), logPath: filepath.Join(t.TempDir(), "worker.log")}
	p.cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir()}
	for _, name := range []string{"AUTH_FILE", "SERVICE_CATALOG_FILE", "PLANNING_CONNECTORS_FILE", "ARCHIVE_DATABASE_FILE", "SIMULATION_FILE", "WORKSPACE_ROOT", "GITLAB_BASE_URL", "GITLAB_TOKEN", "GITLAB_CA_FILE"} {
		p.cmd.Env = append(p.cmd.Env, "AGENT_PLATFORM_"+name+"="+os.Getenv("AGENT_PLATFORM_"+name))
	}
	file, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout, p.cmd.Stderr = file, file
	if err = p.cmd.Start(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); _ = file.Close() }()
	t.Cleanup(func() { p.stop(t, false) })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(readLocalFile(t, p.logPath)), "delivery simulation worker ready") {
			return p
		}
		select {
		case <-p.done:
			p.stopped = true
			t.Fatalf("worker startup failed: %s", readLocalFile(t, p.logPath))
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("worker readiness timed out")
	return p
}
func (p *simulationWorker) stop(t *testing.T, crash bool) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if crash {
		_ = p.cmd.Process.Kill()
	} else {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Error("worker shutdown timed out")
	}
}
func pollSimulation(t *testing.T, address string, b delivery.Bundle, id string, ready func(delivery.Simulation) bool) delivery.Simulation {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var run delivery.Simulation
	for time.Now().Before(deadline) {
		if json.Unmarshal(apiNetworkWrite(t, address, "GET", "/api/v1/deliveries/"+b.ID+"/simulations/"+id, "", 200), &run) != nil {
			t.Fatal("invalid run")
		}
		if ready(run) {
			return run
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("simulation timed out: state=%s code=%s operations=%+v", run.State, run.ErrorCode, run.Operations)
	return run
}
func simulationDB(t *testing.T) *pgx.Conn {
	t.Helper()
	var cfg struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(readLocalFile(t, archiveTestFile(t)), &cfg) != nil {
		t.Fatal("database fixture invalid")
	}
	conn, err := pgx.Connect(context.Background(), cfg.URL)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestTemporalSimulationRecoversAPIWorkerAndDuplicateOutbox(t *testing.T) {
	archiveTestFile(t)
	if os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS") == "" {
		t.Skip("Temporal acceptance disabled")
	}
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	installPlanningFixture(t, fixture, true)
	settings := configureSimulationFixture(t, fixture)
	apiBinary, workerBinary := buildAPI(t), buildSimulationWorker(t)
	first := startAPI(t, apiBinary)
	bundle := createSimulationBundle(t, first.address)
	_, run, startBody := approveSimulationFixture(t, first.address, bundle, "response_loss")
	if run.State != "QUEUED" {
		t.Fatal("offline worker did not leave a durable queue entry")
	}
	first.stop(t)
	second := startAPI(t, apiBinary)
	var replay delivery.Simulation
	if json.Unmarshal(apiNetworkWrite(t, second.address, "POST", "/api/v1/deliveries/"+bundle.ID+"/simulations", startBody, 200), &replay) != nil || replay.ID != run.ID {
		t.Fatal("API restart lost simulation request")
	}
	workerOne := startSimulationWorker(t, workerBinary)
	pollSimulation(t, second.address, bundle, run.ID, func(r delivery.Simulation) bool { return len(r.Operations) > 0 && r.Operations[0].State == "UNKNOWN" })
	workerOne.stop(t, true)
	second.stop(t)
	third := startAPI(t, apiBinary)
	workerTwo := startSimulationWorker(t, workerBinary)
	finished := pollSimulation(t, third.address, bundle, run.ID, func(r delivery.Simulation) bool {
		return r.State == "SIMULATED" || r.State == "BLOCKED" || r.State == "FAILED"
	})
	if finished.State != "SIMULATED" || len(finished.Operations) != 2 {
		t.Fatalf("recovery failed: %+v", finished)
	}
	conn := simulationDB(t)
	count := 0
	if conn.QueryRow(context.Background(), `SELECT count(*) FROM agent_delivery.simulation_receipts r JOIN agent_delivery.operations o ON o.id=r.operation_id AND o.tenant_id=r.tenant_id WHERE o.tenant_id=$1 AND o.run_id=$2`, "test", run.ID).Scan(&count) != nil || count != 2 {
		t.Fatal("recovery duplicated simulator effects")
	}
	c, err := client.Dial(client.Options{HostPort: settings.TemporalAddress, Namespace: settings.Namespace})
	if err != nil {
		t.Fatal("Temporal test client unavailable")
	}
	defer c.Close()
	history := c.GetWorkflowHistory(context.Background(), run.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	events := 0
	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			t.Fatal("read Temporal history")
		}
		events++
		payloads := []*commonpb.Payload{}
		if a := event.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			payloads = append(payloads, a.GetInput().GetPayloads()...)
		}
		if a := event.GetActivityTaskScheduledEventAttributes(); a != nil {
			payloads = append(payloads, a.GetInput().GetPayloads()...)
		}
		if a := event.GetActivityTaskCompletedEventAttributes(); a != nil {
			payloads = append(payloads, a.GetResult().GetPayloads()...)
		}
		for _, payload := range payloads {
			for _, forbidden := range []string{"business-value-not-for-temporal-history", "api-gitlab-token", "planning-fixture-token", "api-model-token"} {
				if strings.Contains(string(payload.Data), forbidden) {
					t.Fatal("business values or credentials entered Temporal history")
				}
			}
		}
	}
	if events == 0 {
		t.Fatal("no Temporal history")
	}
	if _, err := conn.Exec(context.Background(), `UPDATE agent_delivery.outbox SET delivered_at=NULL,lease_until=NULL,available_at=clock_timestamp() WHERE tenant_id=$1 AND run_id=$2`, "test", run.ID); err != nil {
		t.Fatal("inject lost outbox acknowledgement")
	}
	deadline := time.Now().Add(10 * time.Second)
	delivered := false
	for time.Now().Before(deadline) {
		if conn.QueryRow(context.Background(), `SELECT delivered_at IS NOT NULL FROM agent_delivery.outbox WHERE tenant_id=$1 AND run_id=$2`, "test", run.ID).Scan(&delivered) == nil && delivered {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !delivered {
		t.Fatal("duplicate publication did not acknowledge existing workflow")
	}
	if conn.QueryRow(context.Background(), `SELECT count(*) FROM agent_delivery.simulation_receipts r JOIN agent_delivery.operations o ON o.id=r.operation_id WHERE o.run_id=$1`, run.ID).Scan(&count) != nil || count != 2 {
		t.Fatal("outbox replay duplicated effects")
	}
	workerTwo.stop(t, false)
	third.stop(t)
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("simulation invoked the model")
	}
}

func TestSimulationReloadsAuthorityAndChecksFrozenInputsBeforeEffects(t *testing.T) {
	archiveTestFile(t)
	if os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS") == "" {
		t.Skip("simulation deployment acceptance disabled")
	}
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installPlanningFixture(t, fixture, true)
	configureSimulationFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	bundle := createSimulationBundle(t, process.address)
	service, _, err := apiapp.SimulationWorkerFromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Store.Close()
	for _, scenario := range []string{"permission", "repository", "resource"} {
		t.Run(scenario, func(t *testing.T) {
			_, run, _ := approveSimulationFixture(t, process.address, bundle, "success")
			catalogFile := os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")
			original := readLocalFile(t, catalogFile)
			switch scenario {
			case "permission":
				var catalog servicecatalog.Config
				if json.Unmarshal(original, &catalog) != nil {
					t.Fatal("catalog fixture invalid")
				}
				for i := range catalog.Grants {
					catalog.Grants[i].Operations = []string{"read", "create", "snapshot", "review", "plan", "archive", "approve_simulation"}
				}
				data, _ := json.Marshal(catalog)
				if os.WriteFile(catalogFile, data, 0o600) != nil {
					t.Fatal("update fixture grants")
				}
				defer func() { _ = os.WriteFile(catalogFile, original, 0o600) }()
			case "repository":
				marker := filepath.Join(fixture.directory, "source-drift")
				if os.WriteFile(marker, []byte("changed"), 0o600) != nil {
					t.Fatal("write drift fixture")
				}
				defer func() { _ = os.Remove(marker) }()
			case "resource":
				platform.revision.Store(2)
				defer platform.revision.Store(1)
			}
			ref := delivery.Ref{TenantID: bundle.TenantID, RunID: run.ID}
			ids, err := service.Begin(context.Background(), ref)
			if err != nil || len(ids) != 0 {
				t.Fatalf("untrusted input began: %v", err)
			}
			state, err := service.Store.FinishSimulation(context.Background(), ref)
			if err != nil || state != "BLOCKED" {
				t.Fatal("changed input not blocked")
			}
			var stored delivery.Simulation
			if json.Unmarshal(apiNetworkWrite(t, process.address, "GET", "/api/v1/deliveries/"+bundle.ID+"/simulations/"+run.ID, "", 200), &stored) != nil || len(stored.Operations) != 0 {
				t.Fatal("blocked run created operations")
			}
		})
	}
	path := "/api/v1/deliveries/" + bundle.ID + "/simulation-approvals"
	for _, bad := range []map[string]any{
		{"idempotencyKey": rand.Text(), "expectedDigest": bundle.Digest, "ttlSeconds": 3600, "reason": "fixture", "findingsReviewed": false},
		{"idempotencyKey": rand.Text(), "expectedDigest": bundle.Digest, "ttlSeconds": 3600, "reason": "fixture", "findingsReviewed": true, "mode": "REAL"},
		{"idempotencyKey": rand.Text(), "expectedDigest": bundle.Digest, "ttlSeconds": 3600, "reason": "fixture", "findingsReviewed": true, "subject": "someone-else"},
	} {
		data, _ := json.Marshal(bad)
		apiNetworkWrite(t, process.address, "POST", path, string(data), 400)
	}
	settingsFile := os.Getenv("AGENT_PLATFORM_SIMULATION_FILE")
	settingsData := readLocalFile(t, settingsFile)
	var settings apiapp.SimulationSettings
	if json.Unmarshal(settingsData, &settings) != nil {
		t.Fatal("settings fixture invalid")
	}
	settings.AllowSelfApproval = false
	data, _ := json.Marshal(settings)
	if os.WriteFile(settingsFile, data, 0o600) != nil {
		t.Fatal("update self approval fixture")
	}
	body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedDigest": bundle.Digest, "ttlSeconds": 3600, "reason": "fixture", "findingsReviewed": true})
	apiNetworkWrite(t, process.address, "POST", path, string(body), 403)
	if os.WriteFile(settingsFile, settingsData, 0o600) != nil {
		t.Fatal("restore self approval fixture")
	}
	approval, _, _ := approveSimulationFixture(t, process.address, bundle, "success")
	full, err := service.Store.Get(context.Background(), bundle.TenantID, bundle.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := planning.Clone(full)
	other.ChangeID = "other-change"
	request := delivery.Request{TenantID: bundle.TenantID, ChangeID: other.ChangeID, PlanID: other.Plan.ID, PlanHash: other.Plan.Hash, IdempotencyKey: rand.Text()}
	other, _, err = service.Store.Save(context.Background(), request, other)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]string{"idempotencyKey": rand.Text(), "expectedDigest": other.Digest, "approvalId": approval.ID, "scenario": "success"})
	apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+other.ID+"/simulations", string(body), 404)
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("simulation validation invoked model")
	}
	process.stop(t)
}
