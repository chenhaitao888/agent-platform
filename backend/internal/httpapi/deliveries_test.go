package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/review"
)

func archiveDatabase(t *testing.T) *delivery.Store {
	t.Helper()
	filename := os.Getenv("AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE")
	if filename == "" {
		t.Skip("set AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE for PostgreSQL HTTP tests")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal("cannot read database test file")
	}
	var value struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("invalid database test file")
	}
	store, err := delivery.Open(context.Background(), value.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}
func archiveHTTP(t *testing.T, f *deliveryFixture) http.Handler {
	t.Helper()
	planner, err := planning.NewPlanner(map[string]planning.ConfigReader{"config-ref": &planConfigReader{}}, map[string]planning.ExperimentReader{"ab-ref": &planExperimentReader{}}, planning.NewDiffAnalyzer())
	if err != nil {
		t.Fatal(err)
	}
	return deliveryHandler(t, f, true, planner)
}
func archiveSource(t *testing.T, h http.Handler) planning.Plan {
	t.Helper()
	createDeliverySnapshot(t, h)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	p, _ := planResponse(t, changeRequest(t, h, "POST", "/api/v1/changes/change-1/plans", planBody("plan", 4, uninvolved()), 201))
	return p
}
func archiveBody(p planning.Plan, key string) string {
	data, _ := json.Marshal(map[string]string{"idempotencyKey": key, "expectedPlanHash": p.Hash})
	return string(data)
}
func TestDeliveryHTTPPersistsEvidenceAndReplaysAfterLosingLiveStores(t *testing.T) {
	store := archiveDatabase(t)
	f := &deliveryFixture{archives: store}
	h := archiveHTTP(t, f)
	plan := archiveSource(t, h)
	path := "/api/v1/changes/change-1/plans/plan-1/archive"
	body := archiveBody(plan, rand.Text())
	first := changeRequest(t, h, "POST", path, body, 201)
	var saved delivery.Bundle
	if err := json.Unmarshal(first, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Evidence) != 3 || len(saved.Evidence[0].Content) != 0 || saved.Plan.Hash != plan.Hash || saved.Digest == "" {
		t.Fatal("package omitted evidence or exposed inline artifact content")
	}
	artifactPath := "/api/v1/deliveries/" + saved.ID + "/artifacts/" + saved.Evidence[0].Execution.Artifact.ID + "/content?tenantId=tenant-a"
	content := changeRequest(t, h, "GET", artifactPath, "", 200)
	store.Close()
	next := &deliveryFixture{archives: archiveDatabase(t), findings: []review.Finding{{Title: "Unrelated new finding", Description: "Must not replace saved evidence", Severity: "LOW", Confidence: 0.8, Path: "a.go", StartLine: 1, EndLine: 1}}}
	restarted := archiveHTTP(t, next)
	changeRequest(t, restarted, "GET", "/api/v1/tasks/task-1?tenantId=tenant-a", "", 404)
	if replay := changeRequest(t, restarted, "POST", path, body, 200); string(replay) != string(first) {
		t.Fatal("replay changed after API store restart")
	}
	if got := changeRequest(t, restarted, "GET", "/api/v1/deliveries/"+saved.ID, "", 200); string(got) != string(first) {
		t.Fatal("saved package changed")
	}
	archiveSource(t, restarted)
	if got := changeRequest(t, restarted, "GET", artifactPath, "", 200); string(got) != string(content) || strings.Contains(string(got), "Unrelated") {
		t.Fatal("archive dereferenced a reused live artifact ID")
	}
	if next.runs != 3 {
		t.Fatal("saved evidence read invoked review")
	}
}
func TestDeliveryHTTPAuthorizationAndConcurrentIdempotency(t *testing.T) {
	store := archiveDatabase(t)
	h := archiveHTTP(t, &deliveryFixture{archives: store})
	plan := archiveSource(t, h)
	path := "/api/v1/changes/change-1/plans/plan-1/archive"
	body := archiveBody(plan, rand.Text())
	for subject, status := range map[string]int{"": 401, "reader": 403, "outsider": 403, "bob": 404} {
		if w := deliveryAs(h, subject, "POST", path, body); w.Code != status {
			t.Fatalf("%s: %d %s", subject, w.Code, w.Body.String())
		}
	}
	changeRequest(t, h, "POST", path, strings.Replace(body, `"expectedPlanHash":`, `"payload":{},"expectedPlanHash":`, 1), 400)
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		go func() { responses <- deliveryAs(h, "alice", "POST", path, body) }()
	}
	created := 0
	id := ""
	for range 8 {
		w := <-responses
		if w.Code == 201 {
			created++
		} else if w.Code != 200 {
			t.Fatalf("concurrent save: %d %s", w.Code, w.Body.String())
		}
		var b delivery.Bundle
		if json.Unmarshal(w.Body.Bytes(), &b) != nil {
			t.Fatal("bad response")
		}
		if id != "" && id != b.ID {
			t.Fatal("duplicate package")
		}
		id = b.ID
	}
	if created != 1 {
		t.Fatalf("created %d packages", created)
	}
	changeRequest(t, h, "POST", path, archiveBody(plan, rand.Text()), 200)
	changed := strings.Replace(body, plan.Hash, strings.Repeat("a", 64), 1)
	changeRequest(t, h, "POST", path, changed, 409)
	if w := deliveryAs(h, "reader", "POST", path, changed); w.Code != 403 {
		t.Fatal("conflict bypassed archive authorization")
	}
	for _, p := range []string{"/api/v1/deliveries/" + id, "/api/v1/deliveries/" + id + "/artifacts/artifact-1/content"} {
		if w := deliveryAs(h, "outsider", "GET", p, ""); w.Code != 403 {
			t.Fatal("ungranted package read")
		}
		if w := deliveryAs(h, "bob", "GET", p, ""); w.Code != 404 {
			t.Fatal("cross-tenant package read")
		}
		if w := deliveryAs(h, "reader", "GET", p, ""); w.Code != 200 {
			t.Fatal("authorized historical read failed")
		}
	}
	if w := deliveryAs(h, "outsider", "GET", "/api/v1/deliveries", ""); w.Body.String() != "{\"items\":[]}\n" {
		t.Fatal("list disclosed another service")
	}
	revoked := archiveHTTP(t, &deliveryFixture{archives: store, revokeArchive: true})
	changeRequest(t, revoked, "POST", path, body, 403)
	changeRequest(t, revoked, "GET", "/api/v1/deliveries/"+id, "", 200)
	noRead := archiveHTTP(t, &deliveryFixture{archives: store, revokeRead: true})
	changeRequest(t, noRead, "GET", "/api/v1/deliveries/"+id, "", 403)
}
