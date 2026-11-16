package apiapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/apiapp"
)

const catalogJSON = `{"services":[{"tenantId":"test","serviceId":"S1","name":"订单服务","catalogVersion":"1","repository":{"connectorRef":"gitlab","repositoryId":"42","defaultTargetBranch":"master","allowedTargetBranches":["master","release/test"]},"environments":["test"]}],"grants":[{"subject":"fixture-user","tenantId":"test","serviceId":"S1","environment":"test","operations":["read","create","snapshot"]}]}`

func installCatalog(t *testing.T, data string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", name)
	return name
}
func TestServiceCatalogDeploymentIsAvailableWithoutReview(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, false)
	installCatalog(t, catalogJSON)
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if app.ReviewEnabled || app.ListenAddress != "127.0.0.1:8080" {
		t.Fatal("D1 changed deployment boundaries")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/services", nil)
	r.Header.Set("Authorization", "Bearer "+testAPIToken)
	app.Handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "订单服务") || strings.Contains(w.Body.String(), "repositoryId") {
		t.Fatalf("catalog response: %d %s", w.Code, w.Body.String())
	}
	create := httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/v1/changes", strings.NewReader(`{"idempotencyKey":"create","title":"A","environment":"test","services":[{"serviceId":"S1","sourceBranch":"master"}]}`))
	r.Header.Set("Authorization", "Bearer "+testAPIToken)
	app.Handler.ServeHTTP(create, r)
	if create.Code != 201 {
		t.Fatal(create.Body.String())
	}
	var record map[string]any
	_ = json.Unmarshal(create.Body.Bytes(), &record)
	snapshot := httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/v1/changes/"+record["id"].(string)+"/snapshots", strings.NewReader(`{"idempotencyKey":"snapshot","expectedVersion":1,"services":[{"serviceId":"S1","sourceBranch":"master"}]}`))
	r.Header.Set("Authorization", "Bearer "+testAPIToken)
	app.Handler.ServeHTTP(snapshot, r)
	if snapshot.Code != 201 || !strings.Contains(snapshot.Body.String(), `"state":"READY"`) {
		t.Fatalf("snapshot response: %d %s", snapshot.Code, snapshot.Body.String())
	}
	if fixture.modelCalls.Load() != 0 {
		t.Fatal("D1 called a model")
	}
}
func TestServiceCatalogDeploymentRejectsUnregisteredAndAmbiguousConfiguration(t *testing.T) {
	for _, mode := range []string{"unknown-field", "duplicate-key", "case", "null", "second-json", "unknown-repository", "unknown-connector", "unknown-subject", "unknown-environment", "production", "missing-default", "bad-branch", "duplicate-service", "duplicate-grant", "write-grant", "missing-read", "empty-services", "relative", "permissions", "symlink", "workspace", "no-auth", "no-gitlab"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, false)
			data := catalogJSON
			switch mode {
			case "unknown-field":
				data = strings.Replace(data, `"name"`, `"url":"secret-value","name"`, 1)
			case "duplicate-key":
				data = strings.Replace(data, `"repositoryId"`, `"repositoryId":"99","repositoryId"`, 1)
			case "case":
				data = strings.Replace(data, `"serviceId"`, `"ServiceId"`, 1)
			case "null":
				data = strings.Replace(data, `"environments":["test"]`, `"environments":null`, 1)
			case "second-json":
				data += `{}`
			case "unknown-repository":
				data = strings.ReplaceAll(data, `"42"`, `"99"`)
			case "unknown-connector":
				data = strings.ReplaceAll(data, `"gitlab"`, `"gitlab-other"`)
			case "unknown-subject":
				data = strings.ReplaceAll(data, `"fixture-user"`, `"nobody"`)
			case "unknown-environment":
				data = strings.Replace(data, `"environment":"test"`, `"environment":"staging"`, 1)
			case "production":
				data = strings.Replace(data, `"environments":["test"]`, `"environments":["prod"]`, 1)
			case "missing-default":
				data = strings.Replace(data, `"defaultTargetBranch":"master"`, `"defaultTargetBranch":"main"`, 1)
			case "bad-branch":
				data = strings.ReplaceAll(data, "release/test", "../bad")
			case "duplicate-service", "duplicate-grant":
				var v map[string][]json.RawMessage
				_ = json.Unmarshal([]byte(data), &v)
				key := "services"
				if mode == "duplicate-grant" {
					key = "grants"
				}
				v[key] = append(v[key], v[key][0])
				raw, _ := json.Marshal(v)
				data = string(raw)
			case "write-grant":
				data = strings.Replace(data, `"snapshot"`, `"write"`, 1)
			case "missing-read":
				data = strings.Replace(data, `"read",`, "", 1)
			case "empty-services":
				data = `{"services":null,"grants":[]}`
			}
			name := installCatalog(t, data)
			switch mode {
			case "relative":
				t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", "catalog.json")
			case "permissions":
				if err := os.Chmod(name, 0o666); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(name, name+".link"); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", name+".link")
			case "workspace":
				if err := os.MkdirAll(fixture.root, 0o700); err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(fixture.root, "catalog.json")
				if err := os.WriteFile(destination, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", destination)
			case "no-auth":
				t.Setenv("AGENT_PLATFORM_AUTH_FILE", "")
			case "no-gitlab":
				for _, key := range []string{"GITLAB_BASE_URL", "GITLAB_TOKEN", "WORKSPACE_ROOT", "GITLAB_CA_FILE"} {
					t.Setenv("AGENT_PLATFORM_"+key, "")
				}
			}
			app, err := apiapp.FromEnvironment(context.Background())
			if err == nil || app != nil || strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("invalid catalog accepted or disclosed: %v", err)
			}
		})
	}
}
func TestNoCatalogKeepsChangesClosedAndLegacyAPIAvailable(t *testing.T) {
	cleanDeployment(t)
	configureAPIAuthentication(t, apiCredential("fixture-user", "test", testAPIToken, "42"))
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/services", "/api/v1/changes", "/api/v1/tasks?tenantId=test"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+testAPIToken)
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "{\"items\":[]}\n" {
			t.Fatalf("empty deployment: %s %d %s", path, w.Code, w.Body.String())
		}
	}
}
