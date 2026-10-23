package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/httpapi"
)

func TestGeneratedCredentialsAuthenticateWithTheDeployment(t *testing.T) {
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "AGENT_PLATFORM_") {
			t.Setenv(key, "")
		}
	}
	directory := filepath.Join(t.TempDir(), "login")
	var output bytes.Buffer
	if err := run([]string{"-dir", directory, "-tenant", "tenant-local", "-subject", "local-developer", "-repository", "42"}, &output); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(readFile(t, filepath.Join(directory, "api-token.txt"))))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		t.Fatal("expected a token generated from 32 random bytes")
	}
	config := readFile(t, filepath.Join(directory, "api-auth.json"))
	var parsed struct {
		Identities []httpapi.Credential `json:"identities"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil || len(parsed.Identities) != 1 {
		t.Fatal("invalid generated authentication configuration")
	}
	digest := sha256.Sum256([]byte(token))
	if parsed.Identities[0].TokenSHA256 != hex.EncodeToString(digest[:]) || strings.Contains(string(config), token) || strings.Contains(output.String(), token) {
		t.Fatal("raw credential exposed or digest inconsistent")
	}
	if string(readFile(t, filepath.Join(directory, "api-curl.conf"))) != "header = \"Authorization: Bearer "+token+"\"\n" {
		t.Fatal("curl credential does not match the login token")
	}
	for _, name := range []string{"", "api-auth.json", "api-token.txt", "api-curl.conf"} {
		info, err := os.Stat(filepath.Join(directory, name))
		want := os.FileMode(0o600)
		if name == "" {
			want = 0o700
		}
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("credentials are not private")
		}
	}
	t.Setenv("AGENT_PLATFORM_AUTH_FILE", filepath.Join(directory, "api-auth.json"))
	app, err := apiapp.FromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		token  string
		status int
	}{{token, 200}, {"", 401}, {strings.Repeat("0", 64), 401}} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, request)
		if response.Code != test.status || strings.Contains(response.Body.String(), token) {
			t.Fatal("unexpected authentication result or credential disclosure")
		}
		if test.status == 200 && !strings.Contains(response.Body.String(), `"tenantId":"tenant-local"`) {
			t.Fatal("session does not bind the configured tenant")
		}
	}
}

func TestInitializationNeverOverwritesAnExistingDirectory(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", "")
	for _, mode := range []string{"bundle", "empty-directory", "file", "symlink", "dangling-symlink"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "existing")
			const marker = "existing-credential-must-remain-unchanged"
			switch mode {
			case "bundle":
				if err := run([]string{"-dir", directory}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			case "empty-directory":
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(directory, []byte(marker), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling-symlink":
				target := filepath.Join(t.TempDir(), "target")
				if mode == "symlink" {
					if err := os.WriteFile(target, []byte(marker), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, directory); err != nil {
					t.Fatal(err)
				}
			}
			var original []byte
			if mode == "bundle" {
				original = readFile(t, filepath.Join(directory, "api-token.txt"))
			}
			var output bytes.Buffer
			if err := run([]string{"-dir", directory}, &output); err == nil || output.Len() != 0 {
				t.Fatal("existing output was accepted")
			}
			if mode == "bundle" && !bytes.Equal(original, readFile(t, filepath.Join(directory, "api-token.txt"))) {
				t.Fatal("initialization replaced an existing credential")
			}
			if (mode == "file" || mode == "symlink") && string(readFile(t, directory)) != marker {
				t.Fatal("initialization overwrote existing data")
			}
		})
	}
}

func TestInitializationRejectsInvalidInputsWithoutCreatingFilesOrEchoingArguments(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", "")
	for _, args := range [][]string{
		{"-tenant", "secret invalid tenant"}, {"-subject", "secret invalid subject"},
		{"-repository", "*"}, {"-repository", "42", "-repository", "42"},
		{"-repository", ""}, {"-workspace-root", "relative-secret"},
		{"-token", "secret-value"}, {"secret-positional"},
	} {
		directory := filepath.Join(t.TempDir(), "new")
		var output bytes.Buffer
		err := run(append([]string{"-dir", directory}, args...), &output)
		if err == nil || strings.Contains(err.Error(), "secret") || output.Len() != 0 {
			t.Fatal("invalid initialization input accepted or echoed")
		}
		if _, err := os.Lstat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid input left credential files")
		}
	}
	for _, directory := range []string{"", "relative", "/", filepath.Join(t.TempDir(), "missing-parent", "new")} {
		if err := run([]string{"-dir", directory}, &bytes.Buffer{}); err == nil {
			t.Fatal("unsafe or unavailable output path accepted")
		}
	}
}

func TestCredentialLocationExcludesGitAndWorkspaceIncludingAliases(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", "")
	for _, kind := range []string{"git", "git-file", "git-alias", "workspace", "workspace-alias", "future-workspace"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			workspace := ""
			if strings.HasPrefix(kind, "git") {
				if kind == "git-file" {
					if err := os.WriteFile(filepath.Join(parent, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(filepath.Join(parent, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				workspace = parent
			}
			if strings.HasSuffix(kind, "alias") {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				parent = alias
			}
			directory := filepath.Join(parent, "new")
			if kind == "future-workspace" {
				workspace = directory
			}
			t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", workspace)
			if err := run([]string{"-dir", directory}, &bytes.Buffer{}); err == nil {
				t.Fatal("credentials placed inside a checkout or workspace")
			}
			if _, err := os.Lstat(directory); !os.IsNotExist(err) {
				t.Fatal("rejected path still contains credentials")
			}
		})
	}
	parent := t.TempDir()
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", filepath.Join(parent, "workspace"))
	if err := run([]string{"-dir", filepath.Join(parent, "workspace-credentials")}, &bytes.Buffer{}); err != nil {
		t.Fatal("path-prefix sibling was mistaken for the workspace")
	}
}

func TestConcurrentInitializationReservesOneCompleteCredentialBundle(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", "")
	directory := filepath.Join(t.TempDir(), "login")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- run([]string{"-dir", directory}, &bytes.Buffer{})
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("concurrent initialization did not reserve exactly one directory")
	}
	token := strings.TrimSpace(string(readFile(t, filepath.Join(directory, "api-token.txt"))))
	digest := sha256.Sum256([]byte(token))
	if !bytes.Contains(readFile(t, filepath.Join(directory, "api-auth.json")), []byte(hex.EncodeToString(digest[:]))) ||
		!bytes.Contains(readFile(t, filepath.Join(directory, "api-curl.conf")), []byte(token)) {
		t.Fatal("concurrent initialization mixed tokens from different invocations")
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := run([]string{"-dir", other}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(readFile(t, filepath.Join(other, "api-token.txt")))) == token {
		t.Fatal("independent initialization reused a credential")
	}
	var config struct {
		Identities []httpapi.Credential `json:"identities"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(directory, "api-auth.json")), &config); err != nil || len(config.Identities) != 1 || config.Identities[0].RepositoryIDs == nil || len(config.Identities[0].RepositoryIDs) != 0 {
		t.Fatal("default initialization must not authorize any repository")
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
