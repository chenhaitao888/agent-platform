package apiapp_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/servicecatalog"
)

type planningFixture struct {
	reads    *atomic.Int32
	filename string
}

func installPlanningFixture(t *testing.T, fixture deploymentFixture) planningFixture {
	t.Helper()
	reads := new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer planning-fixture-token" || r.URL.Query().Get("tenantId") != "test" || r.URL.Query().Get("environment") != "test" {
			t.Error("unexpected planning method, credentials or scope")
			w.WriteHeader(403)
			return
		}
		count := reads.Add(1)
		version := "v1"
		if count > 2 {
			version = "v2"
		}
		kind, connector := "config", "config-ref"
		if r.URL.Path == "/v1/experiment" {
			kind, connector = "experiment", "ab-ref"
		} else if r.URL.Path != "/v1/config" {
			t.Error("unknown reference path")
			w.WriteHeader(404)
			return
		}
		target := planning.Target{TenantID: "test", Environment: "test", ConnectorRef: connector, ResourceRef: r.URL.Query().Get("resourceRef")}
		if kind == "config" {
			if target.ResourceRef != "shared/settings" {
				t.Error("unregistered config resource")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": "config-read.v1", "state": planning.ConfigState{Target: target, Version: version, SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{{Key: "feature.timeout", Version: version, Value: planning.Value{Type: "integer", Text: "100"}}}}})
		} else {
			if target.ResourceRef != "checkout-app" {
				t.Error("unregistered experiment resource")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": "experiment-read.v1", "state": planning.ExperimentState{Target: target, Version: version, DraftNoTraffic: true, ConditionalUpdates: true, SupportsExclusion: true, AllocationModes: []string{"weighted"}, Items: []planning.ExperimentItem{}}})
		}
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(fixture.directory, "planning-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(fixture.directory, "planning-token.json")
	if err := os.WriteFile(credential, []byte(`{"token":"planning-fixture-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	connections := []map[string]string{}
	for _, kind := range []string{"config", "experiment"} {
		id := "config-ref"
		if kind == "experiment" {
			id = "ab-ref"
		}
		connections = append(connections, map[string]string{"id": id, "kind": kind, "version": "1", "baseUrl": server.URL, "caFile": ca, "credentialFile": credential})
	}
	data, err := json.Marshal(map[string]any{"connectors": connections})
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(fixture.directory, "planning.json")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE", filename)
	catalog := servicecatalog.Config{Services: []servicecatalog.Entry{}, Grants: []servicecatalog.Grant{}}
	for i, target := range []string{"main", "release/test", "master"} {
		id := fmt.Sprintf("S%d", i+1)
		catalog.Services = append(catalog.Services, servicecatalog.Entry{TenantID: "test", ServiceID: id, Name: id, CatalogVersion: "1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: "42", DefaultTargetBranch: target, AllowedTargetBranches: []string{target}}, Environments: []string{"test"}, Planning: []planning.EnvironmentBinding{{Environment: "test", Config: &planning.ResourceBinding{ConnectorRef: "config-ref", ResourceRef: "shared/settings"}, Experiment: &planning.ResourceBinding{ConnectorRef: "ab-ref", ResourceRef: "checkout-app"}}}})
		catalog.Grants = append(catalog.Grants, servicecatalog.Grant{Subject: "fixture-user", TenantID: "test", ServiceID: id, Environment: "test", Operations: []string{"read", "create", "snapshot", "review", "plan"}})
	}
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	installCatalog(t, string(data))
	return planningFixture{reads: reads, filename: filename}
}

func TestPlanningDeploymentValidatesBindingsAndPrivateFilesBeforeReads(t *testing.T) {
	for _, scenario := range []string{"valid", "relative", "symlink", "hardlink", "permissions", "workspace", "unknown-field", "unknown-connector", "wrong-kind", "http", "credential-permissions", "no-catalog"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, false)
			planningFixture := installPlanningFixture(t, fixture)
			data := string(readLocalFile(t, planningFixture.filename))
			switch scenario {
			case "relative":
				t.Setenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE", "planning.json")
			case "symlink":
				if err := os.Symlink(planningFixture.filename, planningFixture.filename+".link"); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE", planningFixture.filename+".link")
			case "hardlink":
				if err := os.Link(planningFixture.filename, planningFixture.filename+".link"); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(planningFixture.filename, 0o644); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				if err := os.MkdirAll(fixture.root, 0o700); err != nil {
					t.Fatal(err)
				}
				filename := filepath.Join(fixture.root, "planning.json")
				if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_PLANNING_CONNECTORS_FILE", filename)
			case "unknown-field":
				data = strings.Replace(data, `"connectors":`, `"execute":true,"connectors":`, 1)
			case "unknown-connector":
				data = strings.Replace(data, `"id":"config-ref"`, `"id":"other"`, 1)
			case "wrong-kind":
				data = strings.Replace(data, `"kind":"config"`, `"kind":"experiment"`, 1)
			case "http":
				data = strings.ReplaceAll(data, "https://", "http://")
			case "credential-permissions":
				if err := os.Chmod(filepath.Join(fixture.directory, "planning-token.json"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "no-catalog":
				t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", "")
			}
			if err := os.WriteFile(planningFixture.filename, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			app, err := apiapp.FromEnvironment(context.Background())
			if scenario == "valid" {
				if err != nil || app == nil {
					t.Fatalf("valid config rejected: %v", err)
				}
			} else if err == nil || app != nil {
				t.Fatal("invalid planning deployment accepted")
			}
			if planningFixture.reads.Load() != 0 || fixture.modelCalls.Load() != 0 {
				t.Fatal("startup read platform or model")
			}
		})
	}
}
