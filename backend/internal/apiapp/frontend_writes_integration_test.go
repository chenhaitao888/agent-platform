package apiapp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFrontendWritesOverHTTPAndTemporal(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("enable frontend HTTP integration")
	}
	requireWriteTemporal(t)
	for _, scenario := range []string{"success", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, true)
			platform := installWriteFixture(t, fixture)
			process := startAPI(t, buildAPI(t))
			bundle := createWriteBundle(t, process.address)
			platform.drop.Store(scenario == "unknown")
			worker := startWriteWorkerProcess(t, buildWriteWorker(t))
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal(err)
			}
			frontend, err := filepath.Abs("../../../frontend")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/WriteFlow.http.test.tsx")
			cmd.Dir = frontend
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_WRITE_URL=" + process.address, "AGENT_PLATFORM_WRITE_BUNDLE=" + bundle.ID, "AGENT_PLATFORM_WRITE_SCENARIO=" + scenario}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("write UI: %v\n%s", err, output)
			}
			worker.stop(t, false)
			process.stop(t)
			want := int32(3)
			if scenario == "unknown" {
				want = 1
			}
			if platform.effects.Load() != want || platform.posts.Load() != want || fixture.modelCalls.Load() != 1 {
				t.Fatal("UI duplicated side effect or invoked model")
			}
		})
	}
}
