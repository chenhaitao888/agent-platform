package apiapp_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/servicecatalog"
)

// Real API executable + React fetch + HTTPS adapter; no GitLab, Docker or model
// credentials from the host are read. All three project IDs belong to this fixture.
func TestFrontendChangesOverHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22")
	}
	cleanDeployment(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	gitlab := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" || r.Header.Get("PRIVATE-TOKEN") != "change-fixture-token" {
			t.Error("unexpected GitLab request")
			w.WriteHeader(403)
			return
		}
		if !strings.Contains(r.URL.Path, "/projects/1/") && !strings.Contains(r.URL.Path, "/projects/2/") && !strings.Contains(r.URL.Path, "/projects/3/") {
			t.Error("unregistered project")
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/feature/A"):
			if !strings.HasSuffix(r.URL.EscapedPath(), "/branches/feature%2FA") {
				t.Error("source branch slash was not encoded")
			}
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, strings.Repeat("a", 40))
		case strings.HasSuffix(r.URL.Path, "/branches/feature/new"):
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, strings.Repeat("d", 40))
		case strings.Contains(r.URL.Path, "/branches/"):
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, strings.Repeat("b", 40))
		case strings.HasSuffix(r.URL.Path, "/merge_base"):
			refs := r.URL.Query()["refs[]"]
			if len(refs) != 2 || refs[0] != strings.Repeat("b", 40) || (refs[1] != strings.Repeat("a", 40) && refs[1] != strings.Repeat("d", 40)) {
				t.Error("merge-base received mutable refs")
			}
			_, _ = fmt.Fprintf(w, `{"id":%q}`, strings.Repeat("c", 40))
		default:
			t.Error("unexpected GitLab path")
			http.NotFound(w, r)
		}
	}))
	defer gitlab.Close()
	directory := t.TempDir()
	ca := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gitlab.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	configureAPIAuthentication(t, apiCredential("alice", "test", testAPIToken, "1", "2", "3"), apiCredential("bob", "other", "other-api-test-token-at-least-32-characters", "1", "2", "3"))
	for key, value := range map[string]string{"GITLAB_BASE_URL": gitlab.URL, "GITLAB_TOKEN": "change-fixture-token", "GITLAB_CA_FILE": ca, "WORKSPACE_ROOT": filepath.Join(directory, "workspaces")} {
		t.Setenv("AGENT_PLATFORM_"+key, value)
	}
	config := servicecatalog.Config{Services: []servicecatalog.Entry{}, Grants: []servicecatalog.Grant{}}
	for i, target := range []string{"main", "release/test", "master"} {
		id := fmt.Sprintf("S%d", i+1)
		config.Services = append(config.Services, servicecatalog.Entry{TenantID: "test", ServiceID: id, Name: id, CatalogVersion: "v1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: fmt.Sprint(i + 1), DefaultTargetBranch: target, AllowedTargetBranches: []string{target}}, Environments: []string{"test"}})
		config.Grants = append(config.Grants, servicecatalog.Grant{Subject: "alice", TenantID: "test", ServiceID: id, Environment: "test", Operations: []string{"read", "create", "snapshot"}})
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	installCatalog(t, string(data))
	process := startAPI(t, buildAPI(t))
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/ChangeFlow.http.test.tsx")
	cmd.Dir = frontend
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_CHANGE_URL=" + process.address}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("change HTTP flow: %v\n%s", err, output)
	}
	if reads.Load() != 18 {
		t.Fatalf("expected 2 explicit three-service snapshots (18 reads), got %d", reads.Load())
	}
	process.stop(t)
	logs := string(readLocalFile(t, process.logPath))
	for _, secret := range []string{testAPIToken, "change-fixture-token"} {
		if strings.Contains(logs, secret) {
			t.Fatal("credentials in API logs")
		}
	}
}
