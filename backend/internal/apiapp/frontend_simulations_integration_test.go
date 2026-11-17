package apiapp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFrontendSimulationOverHTTPAndTemporal(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" || os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS") == "" {
		t.Skip("enable frontend, PostgreSQL and Temporal integration")
	}
	archiveTestFile(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	installPlanningFixture(t, fixture, true)
	configureSimulationFixture(t, fixture)
	process := startAPI(t, buildAPI(t))
	bundle := createSimulationBundle(t, process.address)
	worker := startSimulationWorker(t, buildSimulationWorker(t))
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
	command := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/SimulationFlow.http.test.tsx")
	command.Dir = frontend
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "AGENT_PLATFORM_SIMULATION_URL=" + process.address, "AGENT_PLATFORM_SIMULATION_BUNDLE=" + bundle.ID}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("simulation UI: %v\n%s", err, output)
	}
	worker.stop(t, false)
	process.stop(t)
	if fixture.modelCalls.Load() != 1 {
		t.Fatal("simulation UI invoked model")
	}
}
