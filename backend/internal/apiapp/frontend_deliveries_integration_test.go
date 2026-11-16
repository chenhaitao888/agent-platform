package apiapp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/servicecatalog"
)

func archiveTestFile(t *testing.T) string {
	t.Helper()
	filename := os.Getenv("AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE")
	if filename == "" {
		t.Skip("set AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE for PostgreSQL acceptance")
	}
	return filename
}
func configureArchiveFixture(t *testing.T, filename string) {
	t.Helper()
	t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", filename)
	var catalog servicecatalog.Config
	if json.Unmarshal(readLocalFile(t, os.Getenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE")), &catalog) != nil {
		t.Fatal("invalid catalog fixture")
	}
	for i := range catalog.Grants {
		catalog.Grants[i].Operations = append(catalog.Grants[i].Operations, "archive")
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	installCatalog(t, string(data))
}
func TestFrontendDeliverySurvivesAPIProcessRestart(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22")
	}
	databaseFile := archiveTestFile(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	planning := installPlanningFixture(t, fixture)
	configureArchiveFixture(t, databaseFile)
	if err := os.WriteFile(filepath.Join(fixture.directory, "pilot-findings.json"), []byte(`[{"title":"Saved review finding","description":"Preserve this finding across API restart.","severity":"MEDIUM","confidence":0.8,"path":"feature.txt","startLine":1,"endLine":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	receiptFile := filepath.Join(t.TempDir(), "receipt.json")
	title := "archive-" + rand.Text()
	runUI := func(address, phase string) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/DeliveryFlow.http.test.tsx")
		command.Dir = frontend
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_DELIVERY_URL=" + address, "AGENT_PLATFORM_DELIVERY_PHASE=" + phase, "AGENT_PLATFORM_DELIVERY_TITLE=" + title, "AGENT_PLATFORM_DELIVERY_RECEIPT=" + receiptFile}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("delivery UI %s: %v\n%s", phase, err, output)
		}
	}
	binary := buildAPI(t)
	first := startAPI(t, binary)
	runUI(first.address, "save")
	var receipt struct {
		Body   string `json:"body"`
		ID     string `json:"id"`
		Digest string `json:"digest"`
	}
	if json.Unmarshal(readLocalFile(t, receiptFile), &receipt) != nil || receipt.ID == "" {
		t.Fatal("missing archive receipt")
	}
	before := apiNetworkWrite(t, first.address, "GET", "/api/v1/deliveries/"+receipt.ID, "", 200)
	first.stop(t)
	if err := os.Rename(fixture.root, fixture.root+"-previous"); err != nil {
		t.Fatal(err)
	}
	restarted := startAPI(t, binary)
	if empty := apiNetworkWrite(t, restarted.address, "GET", "/api/v1/changes", "", 200); string(empty) != "{\"items\":[]}\n" {
		t.Fatal("fixture did not restart with empty live stores")
	}
	apiNetworkWrite(t, restarted.address, "GET", "/api/v1/tasks/task-1?tenantId=test", "", 404)
	after := apiNetworkWrite(t, restarted.address, "POST", "/api/v1/changes/change-1/plans/plan-1/archive", receipt.Body, 200)
	if string(before) != string(after) {
		t.Fatal("archive replay changed across process restart")
	}
	var saved delivery.Bundle
	if json.Unmarshal(after, &saved) != nil || saved.Digest != receipt.Digest {
		t.Fatal("saved digest changed")
	}
	runUI(restarted.address, "restore")
	if fixture.modelCalls.Load() != 1 || planning.reads.Load() != 0 {
		t.Fatal("archive reads reran review or read planning platforms")
	}
	restarted.stop(t)
	var db struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(readLocalFile(t, databaseFile), &db) != nil {
		t.Fatal("invalid database file")
	}
	parsed, err := url.Parse(db.URL)
	if err != nil {
		t.Fatal("invalid database URL")
	}
	password, _ := parsed.User.Password()
	for _, logfile := range []string{first.logPath, restarted.logPath} {
		logs := string(readLocalFile(t, logfile))
		for _, secret := range []string{password, testAPIToken, "api-model-token", "api-gitlab-token", "planning-fixture-token"} {
			if strings.Contains(logs, secret) {
				t.Fatal("credentials disclosed in API logs")
			}
		}
	}
}
