package apiapp_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/httpapi"
	"agent-platform/backend/internal/servicecatalog"
)

func TestWriteAPIRejectsInjectedAuthorityAndForeignExecutionReferences(t *testing.T) {
	archiveTestFile(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	root := "/api/v1/deliveries/" + b.ID
	base := `"idempotencyKey":"bad","expectedDigest":"` + b.Digest + `","ttlSeconds":900,"reason":"reviewed","findingsReviewed":true`
	for _, field := range []string{`"subject":"someone"`, `"policyHash":"fake"`, `"intent":{}`, `"writer":{}`, `"mode":"WRITE"`, `"completed":[]`, `"idempotencyKey":"duplicate"`, `"FindingsReviewed":true`} {
		apiNetworkWrite(t, process.address, "POST", root+"/write-approvals", "{"+base+","+field+"}", 400)
	}
	apiNetworkWrite(t, process.address, "POST", root+"/write-approvals", "{"+base+`,"tenantId":"another"}`, 403)
	a, run, _ := approveAndStartWrite(t, process.address, b)
	for _, body := range []string{`{"intent":{}}`, `{"operationId":"another"}`, `{"idempotencyKey":"not-a-write"}`} {
		apiNetworkWrite(t, process.address, "POST", root+"/write-executions/"+run.ID+"/reconcile", body, 400)
	}
	service, _, err := apiapp.WriteWorkerFromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Store.Close()
	other, err := service.Store.Get(context.Background(), b.TenantID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	other.ChangeID = "other-" + rand.Text()
	saved, _, err := service.Store.Save(context.Background(), delivery.Request{TenantID: other.TenantID, ChangeID: other.ChangeID, PlanID: other.Plan.ID, PlanHash: other.Plan.Hash, IdempotencyKey: rand.Text()}, other)
	if err != nil {
		t.Fatal(err)
	}
	apiNetworkWrite(t, process.address, "GET", "/api/v1/deliveries/"+saved.ID+"/write-executions/"+run.ID, "", 404)
	body, _ := json.Marshal(map[string]any{"idempotencyKey": "wrong-bundle", "expectedVersion": a.Version + 1, "reason": "stop"})
	apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+saved.ID+"/write-approvals/"+a.ID+"/revoke", string(body), 404)
	path := os.Getenv("AGENT_PLATFORM_AUTH_FILE")
	var auth struct {
		Identities []httpapi.Credential `json:"identities"`
	}
	if json.Unmarshal(readLocalFile(t, path), &auth) != nil {
		t.Fatal("auth")
	}
	token := "other-tenant-token-with-at-least-32-characters"
	hash := sha256.Sum256([]byte(token))
	auth.Identities = append(auth.Identities, httpapi.Credential{Subject: "foreign", TenantID: "foreign", RepositoryIDs: []string{"42"}, TokenSHA256: hex.EncodeToString(hash[:])})
	writePrivateJSON(t, path, auth)
	writeRequestAs(t, process.address, root+"/write-executions/"+run.ID, token, 404)
	if platform.posts.Load() != 0 {
		t.Fatal("boundary requests caused effects")
	}
}

func TestWriteAPIReloadsTokenAndReadPermissionsForRecordsAndEvidence(t *testing.T) {
	archiveTestFile(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	installWriteFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	root := "/api/v1/deliveries/" + b.ID
	_, run, _ := approveAndStartWrite(t, process.address, b)
	path := os.Getenv("AGENT_PLATFORM_AUTH_FILE")
	var auth struct {
		Identities []httpapi.Credential `json:"identities"`
	}
	if json.Unmarshal(readLocalFile(t, path), &auth) != nil {
		t.Fatal("auth")
	}
	token := "rotated-api-token-with-at-least-32-characters"
	hash := sha256.Sum256([]byte(token))
	auth.Identities[0].TokenSHA256 = hex.EncodeToString(hash[:])
	writePrivateJSON(t, path, auth)
	apiNetworkWrite(t, process.address, "GET", root+"/write-executions/"+run.ID, "", 401)
	writeRequestAs(t, process.address, root+"/write-executions/"+run.ID, token, 200)
	writeRequestAs(t, process.address, "/api/v1/session", token, 200)
	catalogPath := os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")
	var catalog servicecatalog.Config
	if json.Unmarshal(readLocalFile(t, catalogPath), &catalog) != nil {
		t.Fatal("catalog")
	}
	remaining := []servicecatalog.Grant{}
	for _, grant := range catalog.Grants {
		if grant.ServiceID != "S1" {
			remaining = append(remaining, grant)
		}
	}
	catalog.Grants = remaining
	writePrivateJSON(t, catalogPath, catalog)
	for _, path := range []string{root, root + "/write-access", root + "/write-approvals", root + "/write-executions/" + run.ID, root + "/artifacts/" + b.Evidence[0].Execution.Artifact.ID + "/content"} {
		writeRequestAs(t, process.address, path, token, 403)
	}
	content := writeRequestAs(t, process.address, "/api/v1/deliveries", token, 200)
	if strings.Contains(string(content), b.ID) {
		t.Fatal("revoked delivery remained listed")
	}
}
func writeRequestAs(t *testing.T, address, path, token string, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		t.Fatalf("current authorization: %d %s", response.StatusCode, data)
	}
	return data
}

func TestWriteWorkerCrashAfterRemoteCommitRecoversOnlyByLookup(t *testing.T) {
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	platform.hold.Store(true)
	binary := buildAPI(t)
	process := startAPI(t, binary)
	b := createWriteBundle(t, process.address)
	_, run, body := approveAndStartWrite(t, process.address, b)
	workerBinary := buildWriteWorker(t)
	worker := startWriteWorkerProcess(t, workerBinary)
	select {
	case <-platform.committed:
	case <-time.After(20 * time.Second):
		t.Fatal("write did not commit")
	}
	worker.stop(t, true)
	platform.hold.Store(false)
	process.stop(t)
	process = startAPI(t, binary)
	apiNetworkWrite(t, process.address, "POST", "/api/v1/deliveries/"+b.ID+"/write-executions", body, 200)
	worker = startWriteWorkerProcess(t, workerBinary)
	completed := pollWrite(t, process.address, b, run.ID, "SUCCEEDED")
	if platform.posts.Load() != 3 || platform.effects.Load() != 3 || len(completed.Operations) != 3 {
		t.Fatal("recovery duplicated an effect")
	}
	worker.stop(t, false)
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("write recovery invoked model")
	}
}

func TestWriteAPIReconcilesUnknownAfterRevocationWithoutContinuing(t *testing.T) {
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	platform.drop.Store(true)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	a, run, _ := approveAndStartWrite(t, process.address, b)
	root := "/api/v1/deliveries/" + b.ID
	worker := startWriteWorkerProcess(t, buildWriteWorker(t))
	pollWrite(t, process.address, b, run.ID, "UNKNOWN")
	worker.stop(t, false)
	body, _ := json.Marshal(map[string]any{"idempotencyKey": rand.Text(), "expectedVersion": a.Version + 1, "reason": "stop unknown run"})
	apiNetworkWrite(t, process.address, "POST", root+"/write-approvals/"+a.ID+"/revoke", string(body), 200)
	changeWriteGrants(t, func(_ []string) []string { return []string{"read"} })
	data := apiNetworkWrite(t, process.address, "POST", root+"/write-executions/"+run.ID+"/reconcile", `{}`, 200)
	var recovered delivery.WriteExecution
	if json.Unmarshal(data, &recovered) != nil || recovered.State != "BLOCKED" || recovered.Operations[0].State != "VERIFIED" || platform.posts.Load() != 1 {
		t.Fatal("readonly reconciliation failed or started remaining actions")
	}
}
