package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

const fixtureGatewayToken = "service-only-fixture-token"

func TestExecRunnerUsesDeploymentGatewayAndServiceCredential(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_MODEL_TOKEN", "personal-token-must-not-be-used")
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	credential := filepath.Join(t.TempDir(), "service-credential.json")
	gateway := codex.GatewayConfig{BaseURL: "https://models.example.test/v1", ServicePrincipal: "pr-review-service", CredentialFile: credential}
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{
		Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "review-model", Timeout: 5 * time.Second, Gateway: &gateway,
	})
	if err != nil {
		t.Fatal("startup must not read credentials or call the gateway: ", err)
	}
	writeGatewayCredential(t, credential, gateway.ServicePrincipal, time.Now().Add(time.Hour))
	if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	capture := fixture.readCapture(t)
	if capture.Env["AGENT_PLATFORM_MODEL_TOKEN"] != fixtureGatewayToken || capture.Env["OPENAI_API_KEY"] != "" || capture.Env["CODEX_API_KEY"] != "" {
		t.Fatal("model CLI did not receive only the selected service credential")
	}
	arguments := strings.Join(capture.Args, " ")
	for _, required := range []string{`model_provider="agent_gateway"`, gateway.BaseURL, `env_key="AGENT_PLATFORM_MODEL_TOKEN"`, `wire_api="responses"`, `requires_openai_auth=false`, `supports_websockets=false`} {
		if !strings.Contains(arguments, required) {
			t.Fatalf("deployment provider setting is missing: %s", required)
		}
	}
	if strings.Contains(arguments+capture.Stdin, fixtureGatewayToken) {
		t.Fatal("credential leaked into argv or model input")
	}
	identity := runner.RuntimeIdentity()
	if identity.GatewayURL != gateway.BaseURL || identity.ServicePrincipal != gateway.ServicePrincipal {
		t.Fatal("execution lost the configured gateway and service identity")
	}
	if _, err := os.Stat(credential); err != nil {
		t.Fatal("execution removed the deployment credential file")
	}
}

func writeGatewayCredential(t *testing.T, path, principal string, expires time.Time) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"servicePrincipal": principal, "token": fixtureGatewayToken, "expiresAt": expires.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDockerRunnerSendsServiceCredentialOnlyThroughWorkerStdin(t *testing.T) {
	fixture := newDockerFixture(t)
	credential := filepath.Join(t.TempDir(), "credential.json")
	config := fixture.config()
	config.Gateway = &codex.GatewayConfig{BaseURL: "https://models.example.test/v1", ServicePrincipal: "review-service", CredentialFile: credential}
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal("probe must not need a credential file: ", err)
	}
	writeGatewayCredential(t, credential, config.Gateway.ServicePrincipal, time.Now().Add(time.Hour))
	if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	commands := fixture.commands(t)
	var envelope struct {
		Input      json.RawMessage `json:"input"`
		Credential struct {
			Token string `json:"token"`
		} `json:"credential"`
	}
	if err := json.Unmarshal([]byte(commands[5].Stdin), &envelope); err != nil || envelope.Credential.Token != fixtureGatewayToken || !strings.Contains(string(envelope.Input), fixture.input().HeadSHA) {
		t.Fatal("Worker stdin did not carry the controlled credential and review input")
	}
	for i, capture := range commands {
		arguments := strings.Join(capture.Args, " ")
		if strings.Contains(arguments, fixtureGatewayToken) || strings.Contains(arguments, credential) || capture.Env["AGENT_PLATFORM_MODEL_TOKEN"] != "" {
			t.Fatal("credential was placed in Docker argv, environment or a host mount")
		}
		if i != 5 && strings.Contains(capture.Stdin, fixtureGatewayToken) {
			t.Fatal("credential reached preflight or cleanup")
		}
	}
	identity := runner.RuntimeIdentity()
	if identity.GatewayURL != config.Gateway.BaseURL || identity.ServicePrincipal != config.Gateway.ServicePrincipal {
		t.Fatal("Docker execution lost the service identity")
	}
}

func TestExecRunnerReadsRotatedStaticKeysWithoutUsingPersonalEnvironment(t *testing.T) {
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	credential := filepath.Join(t.TempDir(), "credential.json")
	gateway := &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "pr-review-deepseek", CredentialFile: credential}
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "deepseek-flash", Timeout: 5 * time.Second, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"first-service-key", "rotated-service-key"} {
		data, _ := json.Marshal(map[string]string{"servicePrincipal": gateway.ServicePrincipal, "token": token})
		if err := os.WriteFile(credential, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
			t.Fatal("static key without an expiry must be supported: ", err)
		}
		if fixture.readCapture(t).Env["AGENT_PLATFORM_MODEL_TOKEN"] != token {
			t.Fatal("runner cached a rotated service credential")
		}
	}
}

func TestExecRunnerRejectsUnusableServiceCredentialsBeforeModelLaunch(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong-principal", "expired", "expires-during-run", "public-file", "symlink", "hardlink", "oversize", "unknown-field", "newline-token"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
			credential := filepath.Join(t.TempDir(), "credential.json")
			principal := "review-service"
			writeGatewayCredential(t, credential, principal, time.Now().Add(time.Hour))
			switch scenario {
			case "missing":
				credential += ".missing"
			case "wrong-principal":
				writeGatewayCredential(t, credential, "another-service", time.Now().Add(time.Hour))
			case "expired":
				writeGatewayCredential(t, credential, principal, time.Now().Add(-time.Hour))
			case "expires-during-run":
				writeGatewayCredential(t, credential, principal, time.Now().Add(time.Second))
			case "public-file":
				if err := os.Chmod(credential, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink":
				link := credential + ".link"
				var err error
				if scenario == "symlink" {
					err = os.Symlink(credential, link)
				} else {
					err = os.Link(credential, link)
				}
				if err != nil {
					t.Fatal(err)
				}
				credential = link
			case "oversize":
				if err := os.WriteFile(credential, []byte(strings.Repeat("x", 8193)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-field", "newline-token":
				data := map[string]string{"servicePrincipal": principal, "token": fixtureGatewayToken}
				if scenario == "unknown-field" {
					data["endpoint"] = "https://unapproved.example.test"
				} else {
					data["token"] += "\nextra"
				}
				encoded, _ := json.Marshal(data)
				if err := os.WriteFile(credential, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "review-model", Timeout: 5 * time.Second, Gateway: &codex.GatewayConfig{BaseURL: "https://models.example.test", ServicePrincipal: principal, CredentialFile: credential}})
			if err != nil {
				t.Fatal(err)
			}
			if report, err := runner.Run(context.Background(), fixture.input()); !errors.Is(err, codex.ErrGatewayCredentialUnavailable) || report.SchemaVersion != "" || strings.Contains(err.Error(), fixtureGatewayToken) {
				t.Fatal("invalid credential was used or exposed")
			}
			if _, err := os.Stat(fixture.capture); !os.IsNotExist(err) {
				t.Fatal("invalid credentials launched model execution")
			}
		})
	}
}

func TestExecRunnerRejectsFindingsThatContainItsServiceKey(t *testing.T) {
	report := strings.ReplaceAll(string(validRunnerReport(t)), "This path dereferences the input before checking it.", `Service key: \u0073ervice-only-fixture-token`)
	fixture := newRunnerFixture(t, "normal", []byte(report))
	credential := filepath.Join(t.TempDir(), "credential.json")
	writeGatewayCredential(t, credential, "review-service", time.Now().Add(time.Hour))
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "review-model", Timeout: 5 * time.Second, Gateway: &codex.GatewayConfig{BaseURL: "https://models.example.test", ServicePrincipal: "review-service", CredentialFile: credential}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := runner.Run(context.Background(), fixture.input()); !errors.Is(err, review.ErrInvalidFindings) || result.SchemaVersion != "" {
		t.Fatal("service key in model output was accepted for publication")
	}
}

func TestDockerRunnerDoesNotSendCredentialsToUnsupportedWorkersOrInvalidExecutions(t *testing.T) {
	fixture := newDockerFixture(t)
	config := fixture.config()
	config.Gateway = &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "review-service", CredentialFile: filepath.Join(t.TempDir(), "missing.json")}
	marker := filepath.Join(fixture.directory, "no-gateway")
	if err := os.WriteFile(marker, []byte("unsupported"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.NewDockerRunner(context.Background(), config); !errors.Is(err, codex.ErrIncompatibleRuntime) {
		t.Fatal("old Worker was allowed to receive service credentials")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	count := len(fixture.commands(t))
	if _, err := runner.Run(context.Background(), fixture.input()); !errors.Is(err, codex.ErrGatewayCredentialUnavailable) {
		t.Fatal("missing credentials were accepted")
	}
	if len(fixture.commands(t)) != count {
		t.Fatal("missing credentials launched a Docker container")
	}
}
