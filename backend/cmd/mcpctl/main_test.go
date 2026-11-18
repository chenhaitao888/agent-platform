package main

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

func TestOperatorDiscoversChecksConfirmsAndProbesOwnMCP(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workspaces")
	if os.Mkdir(root, 0o700) != nil {
		t.Fatal("workspace")
	}
	target := planning.Target{TenantID: "company", Environment: "test", ConnectorRef: "config", ResourceRef: "app/test"}
	p := mcptest.New("local_", planning.ConfigState{Target: target, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{}}, false)
	server := httptest.NewServer(p.Handler())
	defer server.Close()
	c := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "streamable-http", URL: server.URL}}
	catalog, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding := p.Binding(catalog)
	binding.Confirmation = nil
	write := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(path, data, 0o600) != nil {
			t.Fatal("fixture file")
		}
	}
	settings := apiapp.MCPSettings{Connection: c.Connection, BindingFile: filepath.Join(dir, "binding-v1.json"), SkillFile: filepath.Join(dir, "company.md"), Targets: []planning.Target{target}}
	if os.WriteFile(settings.SkillFile, []byte("company fixture"), 0o600) != nil {
		t.Fatal("skill")
	}
	config := filepath.Join(dir, "mcp.json")
	write(config, settings)
	draft := filepath.Join(dir, "proposal.json")
	write(draft, mcpbridge.Proposal{Binding: binding, Questions: []string{}})
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	invoke := func(command string, args ...string) error {
		os.Args = append([]string{"mcpctl", command, "--config", config, "--workspace-root", root}, args...)
		return run()
	}
	if err := invoke("discover", "--out", filepath.Join(dir, "tools.json")); err != nil {
		t.Fatal(err)
	}
	if err := invoke("check", "--draft", draft); err != nil {
		t.Fatal(err)
	}
	if err := invoke("confirm", "--draft", draft, "--by", "owner", "--expected-digest", "wrong", "--out", settings.BindingFile); err == nil {
		t.Fatal("confirmed a different digest")
	}
	if err := invoke("confirm", "--draft", draft, "--by", "owner", "--expected-digest", binding.Digest(), "--out", settings.BindingFile); err != nil {
		t.Fatal(err)
	}
	if err := invoke("probe"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("confirm", "--draft", draft, "--by", "owner", "--expected-digest", binding.Digest(), "--out", settings.BindingFile); err == nil {
		t.Fatal("overwrote old binding")
	}
	info, err := os.Stat(settings.BindingFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("binding permissions")
	}
	saves, submits, queries := p.Counts()
	if saves != 0 || submits != 0 || queries != 1 {
		t.Fatal("onboarding performed mutation", saves, submits, queries)
	}
}
