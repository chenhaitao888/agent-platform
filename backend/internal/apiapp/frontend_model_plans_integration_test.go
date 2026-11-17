package apiapp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFrontendModelPlansOverHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22")
	}
	fixture := newDeploymentFixture(t, "const limitKey = \"checkout.limit\"\nlimit := settings.Integer(limitKey)\n")
	fixture.configure(t, true)
	platform := installPlanningFixture(t, fixture, true)
	t.Setenv("AGENT_PLATFORM_PLANNING_ANALYZER", "model")
	process := startAPI(t, buildAPI(t))
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/ModelPlanFlow.http.test.tsx")
	cmd.Dir = frontend
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_MODEL_PLAN_URL=" + process.address}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("model plan HTTP: %v\n%s", err, output)
	}
	if fixture.modelCalls.Load() != 3 || platform.reads.Load() != 4 {
		t.Fatalf("unexpected model/reader calls: %d/%d", fixture.modelCalls.Load(), platform.reads.Load())
	}
}
