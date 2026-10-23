package apiapp_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the operator workflow with real executables and loopback HTTP. These
// credentials, ports and processes are temporary; no existing service is touched.
func TestLocalAuthInitializationAndRecoveryOverHTTP(t *testing.T) {
	cleanDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	initializer := filepath.Join(t.TempDir(), "authinit")
	build := exec.CommandContext(ctx, "go", "build", "-o", initializer, "./cmd/authinit")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build authinit: %v %s", err, output)
	}
	generate := func(repositories ...string) (string, string) {
		t.Helper()
		directory := filepath.Join(t.TempDir(), "credentials")
		args := []string{"-dir", directory, "-tenant", "pilot-tenant", "-subject", "pilot-user"}
		for _, repository := range repositories {
			args = append(args, "-repository", repository)
		}
		output, err := exec.CommandContext(ctx, initializer, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("initialize local identity: %v", err)
		}
		token := strings.TrimSpace(string(readLocalFile(t, filepath.Join(directory, "api-token.txt"))))
		if token == "" || strings.Contains(string(output), token) {
			t.Fatal("initializer failed to deliver a private credential")
		}
		return filepath.Join(directory, "api-auth.json"), token
	}
	authFile, token := generate("42")
	binary := buildAPI(t)
	unconfigured := startAPI(t, binary)
	localRequest(t, unconfigured.address, "GET", "/healthz", "", "", 200)
	localRequest(t, unconfigured.address, "GET", "/api/v1/session", token, "", 401)
	unconfigured.stop(t)
	logs := string(readLocalFile(t, unconfigured.logPath))
	if !strings.Contains(logs, "Authentication configured=false") || !strings.Contains(logs, "authinit") {
		t.Fatal("unconfigured startup does not explain how to enable local authentication")
	}
	t.Setenv("AGENT_PLATFORM_AUTH_FILE", authFile)
	configured := startAPI(t, binary)
	session := localRequest(t, configured.address, "GET", "/api/v1/session", token, "", 200)
	if !strings.Contains(session, `"subject":"pilot-user"`) || !strings.Contains(session, `"tenantId":"pilot-tenant"`) {
		t.Fatal("generated credential did not bind the configured identity")
	}
	localRequest(t, configured.address, "GET", "/api/v1/session", strings.Repeat("0", 64), "", 401)
	localRequest(t, configured.address, "GET", "/api/v1/tasks?tenantId=other", token, "", 403)
	localRequest(t, configured.address, "GET", "/api/v1/tasks?tenantId=pilot-tenant", token, "", 200)
	create := `{"requestId":"create","idempotencyKey":"create","tenantId":"pilot-tenant","type":"PR_REVIEW","goal":"review","repository":{"provider":"gitlab","repositoryId":"42","headSha":"` + strings.Repeat("1", 40) + `"}}`
	// Successful login does not pretend GitLab or Review is configured.
	localRequest(t, configured.address, "POST", "/api/v1/tasks", token, create, 503)

	replacement, newToken := generate()
	if err := os.WriteFile(authFile, readLocalFile(t, replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	localRequest(t, configured.address, "GET", "/api/v1/session", token, "", 200)
	localRequest(t, configured.address, "GET", "/api/v1/session", newToken, "", 401)
	configured.stop(t)
	if !strings.Contains(string(readLocalFile(t, configured.logPath)), "Authentication configured=true") {
		t.Fatal("configured startup status is missing")
	}
	for _, mode := range []string{"permissions", "malformed"} {
		if mode == "permissions" {
			if err := os.Chmod(authFile, 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Chmod(authFile, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(authFile, []byte("untrusted-secret-content"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		attemptCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		output, err := exec.CommandContext(attemptCtx, binary).CombinedOutput()
		stop()
		if err == nil || strings.Contains(string(output), "listening on") || !strings.Contains(string(output), "invalid API authentication configuration") || strings.Contains(string(output), "untrusted-secret-content") || strings.Contains(string(output), authFile) {
			t.Fatal("invalid authentication was accepted or disclosed during startup")
		}
	}
	if err := os.WriteFile(authFile, readLocalFile(t, replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := startAPI(t, binary)
	localRequest(t, restarted.address, "GET", "/api/v1/session", token, "", 401)
	localRequest(t, restarted.address, "GET", "/api/v1/session", newToken, "", 200)
	// The replacement intentionally has no repository grants.
	localRequest(t, restarted.address, "POST", "/api/v1/tasks", newToken, create, 403)
	restarted.stop(t)
	for _, process := range []*apiProcess{unconfigured, configured, restarted} {
		logs := string(readLocalFile(t, process.logPath))
		if strings.Contains(logs, token) || strings.Contains(logs, newToken) || strings.Contains(logs, authFile) {
			t.Fatal("local startup or request logs disclosed credentials")
		}
	}
}

func readLocalFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func localRequest(t *testing.T, address, method, path, token, body string, want int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != want || (token != "" && strings.Contains(string(content), token)) {
		t.Fatalf("unexpected local API response for %s: status %d, want %d", path, response.StatusCode, want)
	}
	return string(content)
}
