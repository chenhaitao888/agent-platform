package apiapp_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

// Explicitly opt-in: this uses deployment GitLab credentials, real Docker and
// one paid model execution. Ordinary tests never contact those services.
// Use only a dedicated, authorized project with fixed synthetic input.
func TestLiveGitLabPilotThroughAPI(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_GITLAB_LIVE_TEST") != "1" {
		t.Skip("real GitLab/model pilot is opt-in")
	}
	project, base, head := os.Getenv("AGENT_PLATFORM_PILOT_PROJECT"), os.Getenv("AGENT_PLATFORM_PILOT_BASE"), os.Getenv("AGENT_PLATFORM_PILOT_HEAD")
	if project == "" || base == "" || head == "" || os.Getenv("AGENT_PLATFORM_REVIEW_ENABLED") != "true" || strings.TrimSpace(os.Getenv("AGENT_PLATFORM_GITLAB_TOKEN")) == "" {
		t.Fatal("provide a dedicated project, fixed base/head and complete Review deployment")
	}
	evidence := os.Getenv("AGENT_PLATFORM_PILOT_EVIDENCE_DIR")
	if !filepath.IsAbs(evidence) {
		t.Fatal("provide an absolute, new evidence directory")
	}
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal("evidence directory must be new and have an existing parent")
	}
	save := func(name string, data []byte) {
		t.Helper()
		file, err := os.OpenFile(filepath.Join(evidence, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal("cannot create pilot evidence")
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot write pilot evidence")
		}
	}

	// Authentication and Workspace storage are new for each run. Existing local
	// API sessions and their in-memory Tasks remain untouched.
	newToken := func() string {
		value := make([]byte, 32)
		_, _ = rand.Read(value)
		return hex.EncodeToString(value)
	}
	ownerToken, otherToken := newToken(), newToken()
	identity := func(subject, tenant, token string) map[string]any {
		return map[string]any{"subject": subject, "tenantId": tenant, "tokenSha256": fmt.Sprintf("%x", sha256.Sum256([]byte(token))), "repositoryIds": []string{project}}
	}
	auth, err := json.Marshal(map[string]any{"identities": []any{identity("pilot-owner", "pilot", ownerToken), identity("pilot-other", "other", otherToken)}})
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PLATFORM_AUTH_FILE", authPath)
	t.Setenv("AGENT_PLATFORM_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("AGENT_PLATFORM_REVIEW_TIMEOUT", "2m")
	binary := buildAPI(t)
	process := startAPI(t, binary)
	client := &http.Client{Timeout: 130 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, body []byte, token string, want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, process.address+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal("invalid pilot request")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("pilot HTTP request failed")
		}
		defer func() { _ = response.Body.Close() }()
		content, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(content) > 2<<20 || response.StatusCode != want {
			// Do not echo external diagnostics, request bodies or credentials.
			t.Fatalf("pilot %s %s: status %d, want %d", method, path, response.StatusCode, want)
		}
		return content
	}
	encode := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	decode := func(data []byte, value any) {
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatal("invalid pilot response")
		}
	}

	request("GET", "/api/v1/tasks?tenantId=pilot", nil, "", 401)
	creation := encode(map[string]any{"requestId": "pilot-create", "idempotencyKey": "pilot-create", "tenantId": "pilot", "type": "PR_REVIEW", "goal": "Review the fixed pricing change for correctness regressions.",
		"repository": map[string]string{"provider": "gitlab", "repositoryId": project, "headSha": head}})
	created := request("POST", "/api/v1/tasks", creation, ownerToken, 201)
	save("task.json", created)
	var current task.Task
	decode(created, &current)
	if current.Repository.BaseSHA != base || current.Repository.TargetSHA != base || current.Repository.HeadSHA != head {
		t.Fatal("GitLab resolved unexpected immutable references")
	}
	address := "/api/v1/tasks/" + current.ID
	request("PATCH", address, encode(map[string]any{"requestId": "pilot-queue", "idempotencyKey": "pilot-queue", "tenantId": "pilot", "expectedVersion": current.Version, "status": "QUEUED"}), ownerToken, 200)
	request("POST", address+"/workspace", []byte(`{"requestId":"pilot-register","idempotencyKey":"pilot-register","tenantId":"pilot"}`), ownerToken, 201)
	prepared := request("POST", address+"/workspace/prepare", []byte(`{"requestId":"pilot-prepare","idempotencyKey":"pilot-prepare","tenantId":"pilot","expectedVersion":1}`), ownerToken, 200)
	save("workspace.json", prepared)
	var ready workspace.Workspace
	decode(prepared, &ready)
	if ready.State != workspace.StateReady || ready.BaseSHA != base || ready.HeadSHA != head {
		t.Fatal("GitLab HTTPS clone did not produce the expected Workspace")
	}
	patch := request("GET", address+"/workspace/diff?tenantId=pilot", nil, ownerToken, 200)
	save("diff.json", patch)
	var diff review.Diff
	decode(patch, &diff)
	if diff.BaseSHA != base || diff.HeadSHA != head || fmt.Sprintf("%x", sha256.Sum256([]byte(diff.Patch))) != diff.SHA256 {
		t.Fatal("fixed diff failed reference or checksum validation")
	}
	checkWorktree := func() {
		t.Helper()
		for _, check := range []struct {
			args []string
			want string
		}{
			{[]string{"rev-parse", "HEAD"}, head}, {[]string{"status", "--porcelain"}, ""},
		} {
			command := exec.Command("git", append([]string{"-C", ready.Path}, check.args...)...)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0"}
			output, err := command.Output()
			if err != nil || strings.TrimSpace(string(output)) != check.want {
				t.Fatal("pilot worktree changed or is not at the fixed head")
			}
		}
	}
	checkWorktree()

	input := encode(map[string]any{"requestId": "pilot-review", "idempotencyKey": "pilot-review", "tenantId": "pilot", "expectedWorkspaceVersion": ready.Version})
	started := time.Now().UTC()
	result := request("POST", address+"/review", input, ownerToken, 201)
	save("execution.json", result)
	var execution review.Execution
	decode(result, &execution)
	if execution.State != review.ExecutionSucceeded || execution.Artifact == nil || execution.BaseSHA != base || execution.HeadSHA != head {
		t.Fatal("real review did not archive valid Findings")
	}
	content := request("GET", "/api/v1/artifacts/"+execution.Artifact.ID+"/content?tenantId=pilot", nil, ownerToken, 200)
	if fmt.Sprintf("%x", sha256.Sum256(content)) != execution.Artifact.SHA256 || int64(len(content)) != execution.Artifact.SizeBytes {
		t.Fatal("Findings content differs from its metadata")
	}
	if _, err := review.ParseFindings(content, base, head); err != nil {
		t.Fatal("invalid Findings schema or references")
	}
	save("findings.json", content)
	before := request("GET", address+"/events?tenantId=pilot", nil, ownerToken, 200)
	replay := request("POST", address+"/review", input, ownerToken, 200)
	historical := request("GET", address+"/reviews/"+execution.ID+"?tenantId=pilot", nil, ownerToken, 200)
	if !bytes.Equal(result, replay) || !bytes.Equal(result, historical) {
		t.Fatal("replay or historical read changed the saved execution")
	}
	request("POST", "/api/v1/tasks", creation, otherToken, 403)
	for _, path := range []string{address, address + "/workspace", address + "/review", address + "/reviews/" + execution.ID, "/api/v1/artifacts/" + execution.Artifact.ID + "/content"} {
		request("GET", path+"?tenantId=other", nil, otherToken, 404)
	}
	foreignInput := encode(map[string]any{"requestId": "pilot-foreign", "idempotencyKey": "pilot-foreign", "tenantId": "other", "expectedWorkspaceVersion": ready.Version})
	request("POST", address+"/review", foreignInput, otherToken, 404)
	after := request("GET", address+"/events?tenantId=pilot", nil, ownerToken, 200)
	if !bytes.Equal(before, after) {
		t.Fatal("replay or rejected cross-tenant operations changed events")
	}
	save("events.json", after)
	var events struct {
		Items []task.Event `json:"items"`
	}
	decode(after, &events)
	if len(events.Items) != 8 {
		t.Fatal("expected exactly one Task, Workspace preparation and review lifecycle")
	}
	checkWorktree()
	process.stop(t)
	logs, err := os.ReadFile(process.logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{ownerToken, otherToken, os.Getenv("AGENT_PLATFORM_GITLAB_TOKEN")} {
		if secret != "" && bytes.Contains(logs, []byte(secret)) {
			t.Fatal("pilot logs contain a credential")
		}
	}
	program, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	save("verification.json", encode(map[string]any{"gitlabBaseURL": os.Getenv("AGENT_PLATFORM_GITLAB_BASE_URL"), "projectId": project, "baseSha": base, "headSha": head,
		"apiSHA256": fmt.Sprintf("%x", sha256.Sum256(program)), "reviewStartedAt": started, "finishedAt": time.Now().UTC(), "eventCount": len(events.Items),
		"replayMatches": true, "historicalReadMatches": true, "tenantIsolationVerified": true, "credentialLogScanPassed": true, "worktreeUnchanged": true,
		"interpretation": "Synthetic live integration; Findings still require human review. No production quality claim."}))
}
