package apiapp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/apiapp"
)

func TestWriteDeploymentRequiresExplicitPrivateScopeAndSeparateQueue(t *testing.T) {
	archiveTestFile(t)
	for _, mode := range []string{"valid", "disabled", "relative", "permissions", "symlink", "hardlink", "workspace", "unknown-field", "duplicate", "empty-targets", "unknown-target", "target-tenant", "endpoint", "version", "missing-credential", "credential-permissions", "same-queue", "no-archive", "no-auth"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, false)
			platform := installWriteFixture(t, fixture)
			settings := platform.settings
			rewrite := true
			switch mode {
			case "disabled":
				t.Setenv("AGENT_PLATFORM_WRITES_FILE", "")
			case "relative":
				t.Setenv("AGENT_PLATFORM_WRITES_FILE", "writes.json")
			case "permissions":
				if os.Chmod(platform.filename, 0o644) != nil {
					t.Fatal("chmod")
				}
			case "symlink":
				if os.Symlink(platform.filename, platform.filename+".link") != nil {
					t.Fatal("symlink")
				}
				t.Setenv("AGENT_PLATFORM_WRITES_FILE", platform.filename+".link")
			case "hardlink":
				if os.Link(platform.filename, platform.filename+".link") != nil {
					t.Fatal("hardlink")
				}
			case "workspace":
				if os.MkdirAll(fixture.root, 0o700) != nil {
					t.Fatal("mkdir")
				}
				file := filepath.Join(fixture.root, "writes.json")
				writePrivateJSON(t, file, settings)
				t.Setenv("AGENT_PLATFORM_WRITES_FILE", file)
			case "unknown-field":
				data := strings.Replace(string(readLocalFile(t, platform.filename)), `"connectors":`, `"payload":{},"connectors":`, 1)
				if os.WriteFile(platform.filename, []byte(data), 0o600) != nil {
					t.Fatal("fixture")
				}
				rewrite = false
			case "duplicate":
				data := strings.Replace(string(readLocalFile(t, platform.filename)), `"allowSelfApproval":`, `"allowSelfApproval":false,"allowSelfApproval":`, 1)
				if os.WriteFile(platform.filename, []byte(data), 0o600) != nil {
					t.Fatal("fixture")
				}
				rewrite = false
			case "empty-targets":
				settings.Connectors[0].Targets = nil
			case "unknown-target":
				settings.Connectors[0].Targets[0].ResourceRef = "not-registered"
			case "target-tenant":
				settings.Connectors[0].Targets[0].TenantID = "another-tenant"
			case "endpoint":
				settings.Connectors[0].BaseURL = "https://untrusted.invalid"
			case "version":
				settings.Connectors[0].Version = "2"
			case "missing-credential":
				settings.Connectors[0].CredentialFile = ""
			case "credential-permissions":
				if os.Chmod(settings.Connectors[0].CredentialFile, 0o644) != nil {
					t.Fatal("chmod")
				}
			case "same-queue":
				file := filepath.Join(fixture.directory, "simulation.json")
				writePrivateJSON(t, file, apiapp.SimulationSettings{TemporalAddress: settings.TemporalAddress, Namespace: settings.Namespace, TaskQueue: settings.TaskQueue})
				t.Setenv("AGENT_PLATFORM_SIMULATION_FILE", file)
			case "no-archive":
				t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", "")
			case "no-auth":
				t.Setenv("AGENT_PLATFORM_AUTH_FILE", "")
			}
			if rewrite {
				writePrivateJSON(t, platform.filename, settings)
			}
			app, err := apiapp.FromEnvironment(context.Background())
			if app != nil {
				app.Close()
			}
			if mode == "valid" || mode == "disabled" {
				if err != nil || app == nil {
					t.Fatalf("valid deployment: %v", err)
				}
			} else if err == nil || app != nil {
				t.Fatal("invalid write deployment accepted")
			}
			if platform.posts.Load() != 0 || fixture.modelCalls.Load() != 0 {
				t.Fatal("startup performed a write/model call")
			}
			if err != nil && strings.Contains(err.Error(), "token") {
				t.Fatal("credential diagnostic")
			}
		})
	}
}
