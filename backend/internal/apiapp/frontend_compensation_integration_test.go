package apiapp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFrontendCompensationOverHTTPAndTemporal(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("enable frontend HTTP integration")
	}
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	platform := installWriteFixture(t, fixture)
	allowCompensation(t)
	process := startAPI(t, buildAPI(t))
	b := createWriteBundle(t, process.address)
	_, run, _ := approveAndStartWrite(t, process.address, b)
	before := finishOriginalWithoutWorker(t, b, run)
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
	cmd := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/CompensationFlow.http.test.tsx")
	cmd.Dir = frontend
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_COMPENSATION_URL=" + process.address, "AGENT_PLATFORM_COMPENSATION_BUNDLE=" + b.ID}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compensation UI: %v\n%s", err, output)
	}
	worker.stop(t, false)
	after := pollWrite(t, process.address, b, run.ID, "SUCCEEDED")
	if before.EvidenceHash != after.EvidenceHash || platform.posts.Load() != 4 || platform.effects.Load() != 4 || fixture.modelCalls.Load() != 1 {
		t.Fatal("compensation altered original evidence or repeated effects/model")
	}
}
