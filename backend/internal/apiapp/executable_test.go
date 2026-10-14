package apiapp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type apiProcess struct {
	command          *exec.Cmd
	done             chan error
	address, logPath string
	stopped          bool
}

func buildAPI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "api")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/api")
	command.Dir = "../.."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build API: %v %s", err, output)
	}
	return binary
}

func startAPI(t *testing.T, binary string) *apiProcess {
	t.Helper()
	t.Setenv("AGENT_PLATFORM_API_ADDR", "127.0.0.1:0")
	logPath := filepath.Join(t.TempDir(), "api.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	process := &apiProcess{command: exec.Command(binary), done: make(chan error, 1), logPath: logPath}
	process.command.Stdout, process.command.Stderr = log, log
	process.command.Env = os.Environ()
	if err := process.command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	go func() { err := process.command.Wait(); _ = log.Close(); process.done <- err; close(process.done) }()
	t.Cleanup(func() { process.stop(t) })
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case err := <-process.done:
			process.stopped = true
			t.Fatalf("API exited before listening: %v", err)
		case <-deadline.C:
			t.Fatal("API did not start listening")
		case <-poll.C:
			content, _ := os.ReadFile(logPath)
			if _, suffix, ok := strings.Cut(string(content), "agent-platform API listening on "); ok {
				address, _, _ := strings.Cut(suffix, ";")
				process.address = "http://" + strings.TrimSpace(address)
				return process
			}
		}
	}
}

func (process *apiProcess) stop(t *testing.T) {
	t.Helper()
	if process.stopped {
		return
	}
	process.stopped = true
	_ = process.command.Process.Signal(os.Interrupt)
	select {
	case err := <-process.done:
		if err != nil {
			t.Error("API did not shut down cleanly: ", err)
		}
	case <-time.After(5 * time.Second):
		_ = process.command.Process.Kill()
		<-process.done
		t.Error("API did not exit after shutdown")
	}
}

func apiNetworkWrite(t *testing.T, address, method, path, body string, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != want {
		t.Fatalf("%s %s: %d %s %v", method, path, response.StatusCode, content, err)
	}
	return content
}

func prepareAPINetwork(t *testing.T, address string, fixture deploymentFixture) {
	t.Helper()
	body := `{"requestId":"create","idempotencyKey":"create","tenantId":"test","type":"PR_REVIEW","goal":"review","repository":{"provider":"gitlab","repositoryId":"42","headSha":"` + fixture.head + `"}}`
	apiNetworkWrite(t, address, "POST", "/api/v1/tasks", body, 201)
	apiNetworkWrite(t, address, "PATCH", "/api/v1/tasks/task-1", `{"requestId":"queue","idempotencyKey":"queue","tenantId":"test","expectedVersion":1,"status":"QUEUED"}`, 200)
	apiNetworkWrite(t, address, "POST", "/api/v1/tasks/task-1/workspace", `{"requestId":"register","idempotencyKey":"register","tenantId":"test"}`, 201)
	apiNetworkWrite(t, address, "POST", "/api/v1/tasks/task-1/workspace/prepare", `{"requestId":"prepare","idempotencyKey":"prepare","tenantId":"test","expectedVersion":1}`, 200)
}

func TestAPIExecutableUsesDeploymentAndShutsDownAfterReview(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	binary := buildAPI(t)
	process := startAPI(t, binary)
	apiNetworkWrite(t, process.address, "GET", "/healthz", "", 200)
	prepareAPINetwork(t, process.address, fixture)
	content := apiNetworkWrite(t, process.address, "POST", "/api/v1/tasks/task-1/review", `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`, 201)
	var operation struct {
		State    string `json:"state"`
		Artifact struct {
			ID string `json:"id"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(content, &operation); err != nil || operation.State != "SUCCEEDED" || operation.Artifact.ID == "" {
		t.Fatal("real API executable did not archive review")
	}
	apiNetworkWrite(t, process.address, "GET", "/api/v1/artifacts/"+operation.Artifact.ID+"/content?tenantId=test", "", 200)
	process.stop(t)
	logs, err := os.ReadFile(process.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), "api-gitlab-token") || strings.Contains(string(logs), "api-model-token") {
		t.Fatal("API startup logs leaked service credentials")
	}
}
