package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in because ordinary Go validation does not require frontend dependencies.
// This starts the real authenticated API on loopback and runs the React UI in
// jsdom over actual HTTP. Only external CLI inference is controlled.
func TestFrontendReviewFlowOverAuthenticatedHTTP(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_FRONTEND_INTEGRATION") != "1" {
		t.Skip("set AGENT_PLATFORM_FRONTEND_INTEGRATION=1 with Node 22 and frontend dependencies installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	server := httptest.NewServer(authenticatedHandler(t, fixture.handler, fixture.ready.TenantID))
	t.Cleanup(server.Close)
	frontend, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "node_modules/vitest/vitest.mjs", "run", "src/TaskReview.http.test.tsx")
	command.Dir = frontend
	command.Env = append(os.Environ(), "AGENT_PLATFORM_FRONTEND_TEST_URL="+server.URL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("frontend HTTP integration: %v\n%s", err, output)
	}
	if got := len(fixture.captures(t)); got != 2 {
		t.Fatalf("lost-response retry must replay, explicit new round must execute: got %d CLI calls, want 2", got)
	}
	if operation := fixture.latest(t); operation.Artifact == nil || string(operation.State) != "SUCCEEDED" {
		t.Fatal("frontend flow did not save a successful Findings artifact")
	}
}
