package apiapp_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/connector/mcpbridge/mcptest"
	"agent-platform/backend/internal/planning"
)

// Exercise private deployment loading, the actual HTTP business flow and the
// dedicated Worker process, not just the adapter's in-process methods.
func TestCompanyMCPFromRequirementToPublishedEnvironmentOverHTTP(t *testing.T) {
	requireWriteTemporal(t)
	fixture := newDeploymentFixture(t)
	fixture.configure(t, true)
	old := installWriteFixture(t, fixture)
	target := old.config.Target
	p := mcptest.New("our_tools_", planning.ConfigState{Target: target, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: old.config.Items}, false)
	server := httptest.NewServer(p.Handler())
	defer server.Close()
	c := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "streamable-http", URL: server.URL}}
	catalog, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding := p.Binding(catalog)
	bindingFile := filepath.Join(fixture.directory, "mcp-binding.json")
	writePrivateJSON(t, bindingFile, binding)
	skillFile := filepath.Join(fixture.directory, "company-skill.md")
	if os.WriteFile(skillFile, []byte("company fixture"), 0o600) != nil {
		t.Fatal("skill")
	}
	settingsFile := filepath.Join(fixture.directory, "mcp-settings.json")
	writePrivateJSON(t, settingsFile, apiapp.MCPSettings{Connection: c.Connection, BindingFile: bindingFile, SkillFile: skillFile, Targets: []planning.Target{target}})
	var readers struct {
		Connectors []map[string]string `json:"connectors"`
	}
	if json.Unmarshal(readLocalFile(t, os.Getenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE")), &readers) != nil {
		t.Fatal("readers")
	}
	for n, item := range readers.Connectors {
		if item["kind"] == "config" {
			readers.Connectors[n] = map[string]string{"id": item["id"], "version": "1", "kind": "config", "mcpFile": settingsFile}
		}
	}
	writePrivateJSON(t, os.Getenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE"), readers)
	for n, item := range old.settings.Connectors {
		if item.Kind == "config" {
			old.settings.Connectors[n] = apiapp.WriteConnectionSettings{ID: item.ID, Version: "1", Kind: "config", MCPFile: settingsFile, Targets: []planning.Target{target}}
		}
	}
	writePrivateJSON(t, old.filename, old.settings)
	api := startAPI(t, buildAPI(t))
	bundle := createWriteBundle(t, api.address)
	_, run, _ := approveAndStartWrite(t, api.address, bundle)
	if !run.CompanyFlow {
		t.Fatal("company workflow not selected")
	}
	worker := startWriteWorkerProcess(t, buildWriteWorker(t))
	waiting := pollWrite(t, api.address, bundle, run.ID, "WAITING_COMPANY")
	for _, op := range waiting.Operations {
		if op.Company != nil && op.Company.Ticket == nil {
			t.Fatal("missing real ticket")
		}
	}
	worker.stop(t, true) // SIGKILL during durable wait, then a fresh process.
	p.Configure("APPROVED", "PUBLISHED", false, false)
	worker = startWriteWorkerProcess(t, buildWriteWorker(t))
	completed := pollWrite(t, api.address, bundle, run.ID, "SUCCEEDED")
	worker.stop(t, false)
	for _, op := range completed.Operations {
		if op.Company != nil && (op.Company.State != "PUBLISHED" || op.Company.Observation == nil || op.Company.Observation.EffectiveVersion == "") {
			t.Fatal("missing published proof")
		}
	}
	saves, submits, _ := p.Counts()
	if saves != 2 || submits != 2 || old.posts.Load() != 1 {
		t.Fatal("duplicate/missing effects", saves, submits, old.posts.Load())
	}
	// Changing the operator-confirmed binding invalidates existing approval
	// capability, while ordinary read access to saved evidence remains intact.
	binding.Version = "changed-without-confirmation"
	writePrivateJSON(t, bindingFile, binding)
	apiNetworkWrite(t, api.address, "GET", "/api/v1/deliveries/"+bundle.ID+"/write-executions/"+run.ID, "", 200)
	body, _ := json.Marshal(map[string]any{"idempotencyKey": "changed", "expectedDigest": bundle.Digest, "ttlSeconds": 60, "reason": "changed binding", "findingsReviewed": true})
	apiNetworkWrite(t, api.address, "POST", "/api/v1/deliveries/"+bundle.ID+"/write-approvals", string(body), 403)
}
