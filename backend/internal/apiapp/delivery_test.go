package apiapp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/apiapp"
)

func TestArchiveDeploymentChecksPrivateDatabaseFile(t *testing.T) {
	databaseFile := archiveTestFile(t)
	for _, scenario := range []string{"valid", "relative", "symlink", "hardlink", "permissions", "workspace", "unknown-field", "duplicate", "no-catalog"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.configure(t, false)
			installCatalog(t, catalogJSON)
			filename := filepath.Join(fixture.directory, "archive.json")
			data := readLocalFile(t, databaseFile)
			if err := os.WriteFile(filename, data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", filename)
			switch scenario {
			case "relative":
				t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", "archive.json")
			case "symlink":
				if err := os.Symlink(filename, filename+".link"); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", filename+".link")
			case "hardlink":
				if err := os.Link(filename, filename+".link"); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(filename, 0o644); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				if err := os.MkdirAll(fixture.root, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(fixture.root, "archive.json")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AGENT_PLATFORM_ARCHIVE_DATABASE_FILE", path)
			case "unknown-field":
				data = []byte(strings.Replace(string(data), `"url":`, `"override":true,"url":`, 1))
			case "duplicate":
				data = []byte(strings.Replace(string(data), `"url":`, `"url":"invalid","url":`, 1))
			case "no-catalog":
				t.Setenv("AGENT_PLATFORM_SERVICE_CATALOG_FILE", "")
			}
			if err := os.WriteFile(filename, data, 0o600); err != nil {
				t.Fatal(err)
			}
			app, err := apiapp.FromEnvironment(context.Background())
			if app != nil {
				defer app.Close()
			}
			if scenario == "valid" {
				if err != nil || app == nil {
					t.Fatalf("valid archive config rejected: %v", err)
				}
			} else if err == nil || app != nil {
				t.Fatal("invalid archive file accepted")
			}
			if fixture.modelCalls.Load() != 0 {
				t.Fatal("database initialization called model")
			}
		})
	}
}
