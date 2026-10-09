package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

// Opt in with AGENT_PLATFORM_DOCKER_TEST=1 and an immutable, locally present
// AGENT_PLATFORM_DOCKER_BASE. No model connection or credentials are used.
func TestDockerWorkerIntegration(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_DOCKER_TEST") != "1" {
		t.Skip("real Docker test is opt-in")
	}
	endpoint := os.Getenv("AGENT_PLATFORM_DOCKER_HOST")
	base := os.Getenv("AGENT_PLATFORM_DOCKER_BASE")
	if endpoint == "" || base == "" {
		t.Fatal("provide explicit local Docker endpoint and immutable base")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	clientHome := t.TempDir()
	docker := func(t *testing.T, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + clientHome, "DOCKER_CONFIG=" + clientHome, "DOCKER_HOST=" + endpoint}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	contextPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(contextPath, "codex", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	for _, build := range []struct{ pkg, out string }{{"./cmd/reviewworker", "reviewworker"}, {"./internal/codex/testdata/dockerfixture", "codex/bin/codex"}} {
		command := exec.Command(goBinary, "build", "-o", filepath.Join(contextPath, build.out), build.pkg)
		command.Dir = "../.."
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %v %s", err, output)
		}
	}
	dockerfile, err := os.ReadFile("../../../worker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextPath, "Dockerfile"), dockerfile, 0o600); err != nil {
		t.Fatal(err)
	}
	// Resolve the base locally before the builder can attempt any pull.
	baseID := docker(t, "image", "inspect", "--format", "{{.Id}}", base)
	if !strings.Contains(base, "@sha256:") {
		t.Fatal("floating base is forbidden")
	}
	imageFile := filepath.Join(contextPath, "image-id")
	docker(t, "build", "--pull=false", "--network=none", "--build-arg", "BASE_IMAGE="+baseID, "--iidfile", imageFile, contextPath)
	imageBytes, err := os.ReadFile(imageFile)
	if err != nil {
		t.Fatal(err)
	}
	image := strings.TrimSpace(string(imageBytes))
	t.Cleanup(func() { docker(t, "image", "rm", image) })
	baseline := docker(t, "ps", "--all", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}")
	newFixture := func(t *testing.T, timeout time.Duration) reviewExecutionFixture {
		t.Helper()
		return newReviewExecutionFixtureWithRunner(t, "success", timeout, func(root string) review.Runner {
			runner, err := codex.NewDockerRunner(context.Background(), codex.DockerConfig{Binary: binary, Endpoint: endpoint, Image: image, WorkspaceRoot: root, Model: "offline-fixture", Timeout: timeout, UID: os.Getuid(), GID: os.Getgid()})
			if err != nil {
				t.Fatal(err)
			}
			return runner
		})
	}
	t.Run("archive_and_replay", func(t *testing.T) {
		fixture := newFixture(t, 10*time.Second)
		response := fixture.post(context.Background(), "docker-success", "docker-success", fixture.ready.Version)
		if response.Code != http.StatusCreated {
			t.Fatalf("container execution: %d %s", response.Code, response.Body.String())
		}
		operation := fixture.latest(t)
		if operation.State != review.ExecutionSucceeded || operation.Artifact == nil || operation.Runtime == nil || operation.Runtime.ImageID != image || operation.Runtime.Integration != "docker-exec" || operation.Runtime.CodexVersion != "9.999.0" {
			t.Fatalf("missing archived result and image identity: %#v", operation)
		}
		contentResponse := httptest.NewRecorder()
		fixture.handler.ServeHTTP(contentResponse, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+operation.Artifact.ID+"/content?tenantId="+fixture.ready.TenantID, nil))
		report, err := review.ParseFindings(contentResponse.Body.Bytes(), fixture.ready.BaseSHA, fixture.ready.HeadSHA)
		if err != nil || len(report.Findings) != 0 {
			t.Fatalf("container boundary failed: %s %v", contentResponse.Body.String(), err)
		}
		replay := fixture.post(context.Background(), "docker-success", "replay", fixture.ready.Version)
		if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
			t.Fatal("Docker result replay changed")
		}
		if _, err := os.Stat(filepath.Join(fixture.ready.Path, "worker-write")); !os.IsNotExist(err) {
			t.Fatal("container wrote host workspace")
		}
	})
	for _, cancelCall := range []bool{true, false} {
		name := "timeout"
		if cancelCall {
			name = "cancel_with_escaped_child"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t, 5*time.Second)
			if err := os.WriteFile(filepath.Join(fixture.ready.Path, "feature.txt"), []byte("feature-only change\nWORKER_FIXTURE_HANG\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// Review reads committed SHAs, so the fixture selects the hang mode by
			// reading this immutable mounted file rather than modifying the patch.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() { done <- fixture.post(ctx, "docker-hang", "docker-hang", fixture.ready.Version).Code }()
			finished := false
			t.Cleanup(func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("review did not clean up after test failure")
					}
				}
			})
			if cancelCall {
				deadline := time.Now().Add(4 * time.Second)
				found := false
				for time.Now().Before(deadline) {
					names := strings.Fields(docker(t, "ps", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}"))
					for _, container := range names {
						if !strings.Contains(baseline, container) && strings.Contains(docker(t, "top", container, "-eo", "pid,args"), "--escaped-child") {
							found = true
							break
						}
					}
					if found {
						break
					}
					time.Sleep(30 * time.Millisecond)
				}
				if !found {
					cancel()
					t.Fatal("escaped child did not start")
				}
				cancel()
			}
			select {
			case code := <-done:
				finished = true
				want := http.StatusGatewayTimeout
				if cancelCall {
					want = http.StatusRequestTimeout
				}
				if code != want {
					t.Fatalf("expected %d, got %d", want, code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("container did not stop")
			}
			operation := fixture.latest(t)
			if operation.Artifact != nil || operation.Runtime == nil {
				t.Fatal("failed execution lost identity or published result")
			}
		})
	}
	if remaining := docker(t, "ps", "--all", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}"); remaining != baseline {
		t.Fatalf("owned containers left behind: %s", remaining)
	}
	if realImage := os.Getenv("AGENT_PLATFORM_CODEX_IMAGE"); realImage != "" {
		t.Run("real_codex_sandbox_gate_without_inference", func(t *testing.T) {
			runner, err := codex.NewDockerRunner(context.Background(), codex.DockerConfig{Binary: binary, Endpoint: endpoint, Image: realImage, WorkspaceRoot: t.TempDir(), Model: "probe-only", Timeout: time.Minute, UID: os.Getuid(), GID: os.Getgid()})
			if err != nil {
				if !errors.Is(err, codex.ErrIncompatibleRuntime) {
					t.Fatal(err)
				}
				t.Log("Current host rejects the native Codex sandbox; real model execution remains disabled.")
				return
			}
			profile, err := json.Marshal(runner.Profile())
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(profile))
		})
	}
	if remaining := docker(t, "ps", "--all", "--filter", "label=agent-platform.owner=review-worker", "--format", "{{.Names}}"); remaining != baseline {
		t.Fatalf("probe container left behind: %s", remaining)
	}
}
