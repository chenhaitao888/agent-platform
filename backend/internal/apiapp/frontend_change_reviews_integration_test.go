package apiapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/workspace"
)

// API executable, bearer authentication, React, GitLab HTTPS and real Git
// worktrees. Only Docker/worker and inference are controlled local substitutes.
func TestFrontendChangeReviewsOverHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22")
	}
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	config := servicecatalog.Config{Services: []servicecatalog.Entry{}, Grants: []servicecatalog.Grant{}}
	for i, target := range []string{"main", "release/test", "master"} {
		id := fmt.Sprintf("S%d", i+1)
		config.Services = append(config.Services, servicecatalog.Entry{TenantID: "test", ServiceID: id, Name: id, CatalogVersion: "v1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: "42", DefaultTargetBranch: target, AllowedTargetBranches: []string{target}}, Environments: []string{"test"}})
		config.Grants = append(config.Grants, servicecatalog.Grant{Subject: "fixture-user", TenantID: "test", ServiceID: id, Environment: "test", Operations: []string{"read", "create", "snapshot", "review"}})
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	installCatalog(t, string(data))
	for name, content := range map[string]string{
		"pilot-invalid-first": "once",
		"pilot-findings.json": `[{"title":"Controlled service finding","description":"Inspect the feature line.","severity":"MEDIUM","confidence":0.8,"path":"feature.txt","startLine":1,"endLine":1}]`,
	} {
		if err := os.WriteFile(filepath.Join(fixture.directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	process := startAPI(t, buildAPI(t))
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/ChangeReviewFlow.http.test.tsx")
	cmd.Dir = frontend
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_CHANGE_REVIEW_URL=" + process.address}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("change review HTTP flow: %v\n%s", err, output)
	}
	if fixture.modelCalls.Load() != 7 {
		t.Fatalf("expected 3 initial + 1 retry + 3 new-snapshot executions, got %d", fixture.modelCalls.Load())
	}
	for i := 1; i <= 6; i++ {
		var ws workspace.Workspace
		if err := json.Unmarshal(apiNetworkWrite(t, process.address, "GET", fmt.Sprintf("/api/v1/tasks/task-%d/workspace?tenantId=test", i), "", 200), &ws); err != nil {
			t.Fatal(err)
		}
		if ws.State != workspace.StateReady || ws.BaseSHA != fixture.base || ws.HeadSHA != fixture.head || gitFixture(t, ws.Path, "rev-parse", "HEAD") != fixture.head || gitFixture(t, ws.Path, "status", "--porcelain") != "" {
			t.Fatal("review did not preserve a clean fixed worktree")
		}
	}
	process.stop(t)
	logs := string(readLocalFile(t, process.logPath))
	for _, secret := range []string{testAPIToken, "api-gitlab-token", "api-model-token"} {
		if strings.Contains(logs, secret) {
			t.Fatal("credentials in logs")
		}
	}
}
