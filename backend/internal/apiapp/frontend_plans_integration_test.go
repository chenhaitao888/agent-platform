package apiapp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrontendPlansOverHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22")
	}
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	planningFixture := installPlanningFixture(t, fixture)
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
	command := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/ChangePlanFlow.http.test.tsx")
	command.Dir = frontend
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_PLAN_URL=" + process.address}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("plan HTTP flow: %v\n%s", err, output)
	}
	if planningFixture.reads.Load() != 4 || fixture.modelCalls.Load() != 3 {
		t.Fatalf("reads or replay repeated work: platform=%d model=%d", planningFixture.reads.Load(), fixture.modelCalls.Load())
	}
	process.stop(t)
	logs := string(readLocalFile(t, process.logPath))
	for _, secret := range []string{testAPIToken, "api-model-token", "api-gitlab-token", "planning-fixture-token"} {
		if strings.Contains(logs, secret) {
			t.Fatal("credentials in logs")
		}
	}
}
