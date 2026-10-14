package apiapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
)

// The only substituted runtime component is the external inference executable.
// CLI sandbox, Docker daemon, API executable, TLS GitLab/model endpoints, Git,
// Workspace, review orchestration and Artifact storage are real.
func TestAPIExecutableDockerIntegration(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_API_DOCKER_TEST") != "1" {
		t.Skip("real API/Docker test is opt-in")
	}
	endpoint, native := os.Getenv("AGENT_PLATFORM_DOCKER_HOST"), os.Getenv("AGENT_PLATFORM_CODEX_IMAGE")
	if endpoint == "" || native == "" {
		t.Fatal("provide explicit Docker Unix endpoint and immutable native Worker image")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	runDocker := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, docker, args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "DOCKER_CONFIG=" + home, "DOCKER_HOST=" + endpoint}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v %s", args[0], err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if !strings.HasPrefix(native, "sha256:") && !strings.Contains(native, "@sha256:") {
		t.Fatal("floating native Worker image forbidden")
	}
	var images []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
	}
	if err := json.Unmarshal([]byte(runDocker("image", "inspect", native)), &images); err != nil || len(images) != 1 || images[0].OS != "linux" {
		t.Fatal("invalid native Worker")
	}
	contextPath := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(contextPath, "codex-fixture"), "./internal/codex/testdata/dockerfixture")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+images[0].Architecture)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build external inference fixture: %v %s", err, output)
	}
	dockerfile := `ARG BASE_IMAGE
FROM ${BASE_IMAGE}
USER 0
RUN rm /opt/codex/codex
COPY codex-fixture /opt/codex/codex
USER 65534:65534
`
	if err := os.WriteFile(filepath.Join(contextPath, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	imageFile := filepath.Join(contextPath, "image-id")
	runDocker("build", "--pull=false", "--network=none", "--build-arg", "BASE_IMAGE="+images[0].ID, "--iidfile", imageFile, contextPath)
	imageBytes, err := os.ReadFile(imageFile)
	if err != nil {
		t.Fatal(err)
	}
	image := strings.TrimSpace(string(imageBytes))
	t.Cleanup(func() { runDocker("image", "rm", "--no-prune", image) })
	baseline := runDocker("ps", "-a", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}")
	binary := buildAPI(t)
	for _, scenario := range []string{"archive_and_replay", "shutdown_during_model_request"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			for key, value := range map[string]string{"DOCKER_BINARY": docker, "DOCKER_HOST": endpoint, "REVIEW_WORKER_IMAGE": image, "REVIEW_SECCOMP_POLICY": codex.SeccompCodexBwrap} {
				t.Setenv("AGENT_PLATFORM_"+key, value)
			}
			if scenario == "shutdown_during_model_request" {
				t.Setenv("AGENT_PLATFORM_REVIEW_TIMEOUT", "1s")
				if err := os.WriteFile(filepath.Join(fixture.directory, "model-hang"), []byte("hang"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			process := startAPI(t, binary)
			prepareAPINetwork(t, process.address, fixture)
			body := `{"requestId":"review","idempotencyKey":"review","tenantId":"test","expectedWorkspaceVersion":3}`
			if scenario == "archive_and_replay" {
				first := apiNetworkWrite(t, process.address, "POST", "/api/v1/tasks/task-1/review", body, 201)
				var result struct {
					State   string `json:"state"`
					Runtime struct {
						ModelTransport string `json:"modelTransport"`
					} `json:"runtime"`
					Artifact struct {
						ID string `json:"id"`
					} `json:"artifact"`
				}
				if err := json.Unmarshal(first, &result); err != nil || result.State != "SUCCEEDED" || result.Runtime.ModelTransport != "stdio" || result.Artifact.ID == "" {
					t.Fatal("actual API/Docker did not archive its result")
				}
				content := apiNetworkWrite(t, process.address, "GET", "/api/v1/artifacts/"+result.Artifact.ID+"/content?tenantId=test", "", 200)
				if !strings.Contains(string(content), `"findings":[]`) {
					t.Fatal("native sandbox boundary check failed")
				}
				second := apiNetworkWrite(t, process.address, "POST", "/api/v1/tasks/task-1/review", body, 200)
				if string(first) != string(second) || fixture.modelCalls.Load() != 1 {
					t.Fatal("actual API replay called model or changed result")
				}
			} else {
				done := make(chan int, 1)
				go func() {
					request, _ := http.NewRequest("POST", process.address+"/api/v1/tasks/task-1/review", strings.NewReader(body))
					client := &http.Client{Timeout: 5 * time.Second}
					response, err := client.Do(request)
					if err != nil {
						done <- 0
						return
					}
					_ = response.Body.Close()
					done <- response.StatusCode
				}()
				deadline := time.Now().Add(3 * time.Second)
				for fixture.modelCalls.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if fixture.modelCalls.Load() == 0 {
					t.Fatal("model request never started")
				}
				process.stop(t)
				select {
				case status := <-done:
					if status != 504 {
						t.Fatalf("shutdown lost review response: %d", status)
					}
				case <-time.After(time.Second):
					t.Fatal("model review survived shutdown")
				}
			}
			process.stop(t)
			if remaining := runDocker("ps", "-a", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}"); remaining != baseline {
				t.Fatal("API left owned Docker Workers behind")
			}
		})
	}
}
