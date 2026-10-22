package apiapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/httpapi"
)

const testAPIToken = "api-test-token-with-at-least-32-characters"

func apiCredential(subject, tenant, token string, repos ...string) httpapi.Credential {
	digest := sha256.Sum256([]byte(token))
	return httpapi.Credential{Subject: subject, TenantID: tenant, TokenSHA256: hex.EncodeToString(digest[:]), RepositoryIDs: repos}
}

func configureAPIAuthentication(t *testing.T, credentials ...httpapi.Credential) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "api-auth.json")
	content, err := json.Marshal(map[string]any{"identities": credentials})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PLATFORM_AUTH_FILE", filename)
	return filename
}

func TestAuthenticationDeploymentFailsClosedWithoutLeakingConfiguration(t *testing.T) {
	for _, mode := range []string{"relative", "missing", "permissions", "symlink", "hardlink", "directory", "fifo", "oversize", "malformed", "duplicate", "case", "null", "unknown", "workspace", "workspace-symlink", "duplicate-token", "inconsistent-policy"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			filename := configureAPIAuthentication(t, apiCredential("test-user", "test", testAPIToken, "42"))
			data, _ := os.ReadFile(filename)
			switch mode {
			case "relative":
				filename = "untrusted-secret-path"
			case "missing":
				filename += "-untrusted-secret-path"
			case "permissions":
				if err := os.Chmod(filename, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filename + ".link"
				if err := os.Symlink(filename, link); err != nil {
					t.Fatal(err)
				}
				filename = link
			case "hardlink":
				if err := os.Link(filename, filename+".link"); err != nil {
					t.Fatal(err)
				}
			case "directory":
				filename = filepath.Dir(filename)
			case "fifo":
				filename = makeAuthFIFO(t)
			case "oversize":
				data = []byte(strings.Repeat("x", (64<<10)+1))
			case "malformed":
				data = []byte("untrusted-secret-content")
			case "duplicate":
				data = []byte(strings.Replace(string(data), `"tenantId":`, `"tenantId":"other","tenantId":`, 1))
			case "case":
				data = []byte(strings.Replace(string(data), `"tenantId":`, `"TenantId":`, 1))
			case "null":
				data = []byte(`{"identities":null}`)
			case "unknown":
				data = []byte(`{"identities":[],"untrusted-secret-content":true}`)
			case "workspace", "workspace-symlink":
				if err := os.MkdirAll(fixture.root, 0o700); err != nil {
					t.Fatal(err)
				}
				filename = filepath.Join(fixture.root, "auth.json")
				if err := os.WriteFile(filename, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "workspace-symlink" {
					link := filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(fixture.root, link); err != nil {
						t.Fatal(err)
					}
					filename = filepath.Join(link, "auth.json")
				}
			case "duplicate-token":
				filename = configureAPIAuthentication(t, apiCredential("first", "test", testAPIToken, "42"), apiCredential("second", "other", testAPIToken, "43"))
			case "inconsistent-policy":
				filename = configureAPIAuthentication(t, apiCredential("first", "test", testAPIToken, "42"), apiCredential("second", "test", strings.Repeat("b", 40), "43"))
			}
			if mode == "oversize" || mode == "malformed" || mode == "duplicate" || mode == "case" || mode == "null" || mode == "unknown" {
				if err := os.WriteFile(filename, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("AGENT_PLATFORM_AUTH_FILE", filename)
			app, err := apiapp.FromEnvironment(context.Background())
			if app != nil || err == nil || strings.Contains(err.Error(), "untrusted-secret") || strings.Contains(err.Error(), filename) {
				t.Fatalf("unsafe configuration accepted/echoed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(fixture.directory, "commands.jsonl")); !os.IsNotExist(err) {
				t.Fatal("invalid auth reached Docker")
			}
		})
	}
}

func TestAuthenticationAndLoopbackApplyWithoutReview(t *testing.T) {
	cleanDeployment(t)
	for _, address := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", "localhost:8080"} {
		t.Setenv("AGENT_PLATFORM_API_ADDR", address)
		if app, err := apiapp.FromEnvironment(context.Background()); err == nil || app != nil {
			t.Fatal("expanded listener accepted")
		}
	}
	t.Setenv("AGENT_PLATFORM_API_ADDR", "")
	filename := configureAPIAuthentication(t, apiCredential("local", "tenant-local", testAPIToken, "42"))
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if app.ListenAddress != "127.0.0.1:8080" {
		t.Fatal("default listener is not loopback")
	}
	// A running instance keeps its startup policy; rotation requires restart.
	if err := os.WriteFile(filename, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	app.Handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"tenantId":"tenant-local"`) || strings.Contains(response.Body.String(), testAPIToken) {
		t.Fatal("trusted session unavailable or credential leaked")
	}
}

func makeAuthFIFO(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "auth.fifo")
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}
