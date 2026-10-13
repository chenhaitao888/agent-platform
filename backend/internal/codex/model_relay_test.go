package codex_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

func TestDockerModelRelayUsesOnlyDeploymentEndpointAndKeepsUpstreamKeyOnHost(t *testing.T) {
	fixture := newDockerFixture(t)
	requests := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.RequestURI() != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+fixtureGatewayToken {
			t.Error("model request lost deployment endpoint or service authentication")
		}
		requests <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(filepath.Join(fixture.directory, "first-model-chunk")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Error("response was buffered instead of streamed")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	credential := filepath.Join(t.TempDir(), "credential.json")
	writeGatewayCredential(t, credential, "review-service", time.Now().Add(time.Hour))
	config := fixture.config()
	config.Gateway = &codex.GatewayConfig{BaseURL: upstream.URL + "/v1", ServicePrincipal: "review-service", CredentialFile: credential}
	config.ModelRelay = true
	config.ModelHTTPClient = upstream.Client()
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-requests:
		if !strings.Contains(body, `"model":"deployment-review-model"`) {
			t.Fatal("unexpected upstream model")
		}
	default:
		t.Fatal("Worker never reached the controlled model endpoint")
	}
	for _, command := range fixture.commands(t) {
		if strings.Contains(strings.Join(command.Args, " ")+command.Stdin, fixtureGatewayToken) {
			t.Fatal("upstream service key crossed into the Worker")
		}
	}
	if runner.Profile().ModelTransport != "stdio" {
		t.Fatal("deployment transport identity missing")
	}
	if _, err := os.Stat(credential); err != nil {
		t.Fatal(err)
	}
}

func TestModelRelayWorkerAuthenticatesLoopbackAndReturnsFramedFindings(t *testing.T) {
	fixture := newRunnerFixture(t, "model-relay", validRunnerReport(t))
	workerInput, hostOutput := io.Pipe()
	hostInput, workerOutput := io.Pipe()
	defer func() { _ = hostOutput.Close() }()
	defer func() { _ = hostInput.Close() }()
	config := codex.ExecConfig{Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "deployment-model", Timeout: 5 * time.Second, Gateway: &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "review-service"}}
	done := make(chan error, 1)
	go func() {
		err := codex.ServeModelRelayWorker(context.Background(), config, false, workerInput, workerOutput)
		_ = workerOutput.Close()
		done <- err
	}()
	encoder := json.NewEncoder(hostOutput)
	if err := encoder.Encode(map[string]any{"input": fixture.input(), "credential": map[string]string{"servicePrincipal": "review-service", "token": "execution-only-nonce"}}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(hostInput)
	if !scanner.Scan() {
		t.Fatal("no model request")
	}
	var request struct {
		Type    string `json:"type"`
		Request struct {
			Path string `json:"path"`
			Body []byte `json:"body"`
		} `json:"request"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || request.Type != "request" || request.Request.Path != "/responses" || !bytes.Contains(request.Request.Body, []byte("deployment-model")) {
		t.Fatal("bad Worker relay request")
	}
	for _, frame := range []map[string]any{{"type": "head", "head": map[string]any{"status": 200, "contentType": "text/event-stream"}}, {"type": "chunk", "data": []byte("data: [DONE]\n\n")}, {"type": "end"}} {
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	if !scanner.Scan() {
		t.Fatal("no framed result")
	}
	var result struct {
		Type   string `json:"type"`
		Result struct {
			Report json.RawMessage `json:"report"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &result); err != nil || result.Type != "result" {
		t.Fatal("invalid result frame")
	}
	if _, err := review.ParseFindings(result.Result.Report, fixture.input().BaseSHA, fixture.input().HeadSHA); err != nil {
		t.Fatalf("Worker did not finish: %s %v", scanner.Bytes(), err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker stayed alive after result")
	}
}

func relayTestConfig(t *testing.T, fixture dockerFixture, upstream *httptest.Server) codex.DockerConfig {
	t.Helper()
	credential := filepath.Join(t.TempDir(), "credential.json")
	writeGatewayCredential(t, credential, "review-service", time.Now().Add(time.Hour))
	config := fixture.config()
	config.Gateway = &codex.GatewayConfig{BaseURL: upstream.URL + "/v1", ServicePrincipal: "review-service", CredentialFile: credential}
	config.ModelRelay, config.ModelHTTPClient = true, upstream.Client()
	return config
}

func TestDockerModelRelayRejectsWorkerRoutingOverridesBeforeAnyNetworkRequest(t *testing.T) {
	for _, mode := range []string{"bad-path", "bad-method", "bad-model", "unknown-field", "duplicate-model", "oversize-request"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unapproved model request reached upstream") }))
			defer upstream.Close()
			fixture := newDockerFixture(t)
			config := relayTestConfig(t, fixture, upstream)
			runner, err := codex.NewDockerRunner(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte(mode), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(context.Background(), fixture.input())
			if !errors.Is(err, review.ErrInvalidFindings) || report.SchemaVersion != "" {
				t.Fatalf("routing override accepted: %v", err)
			}
			commands := fixture.commands(t)
			if commands[len(commands)-1].Args[0] != "rm" {
				t.Fatal("rejected relay left its Worker behind")
			}
		})
	}
}

func TestDockerModelRelayNeverFollowsRedirectsOrReturnsUpstreamErrorBodies(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("model credential followed a redirect") }))
	defer other.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", other.URL+"/responses")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, fixtureGatewayToken+" private upstream diagnostic")
			}))
			defer upstream.Close()
			fixture := newDockerFixture(t)
			runner, err := codex.NewDockerRunner(context.Background(), relayTestConfig(t, fixture, upstream))
			if err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(context.Background(), fixture.input())
			if !errors.Is(err, review.ErrExecutionFailed) || report.SchemaVersion != "" || strings.Contains(err.Error(), fixtureGatewayToken) {
				t.Fatalf("unsafe upstream response: %v", err)
			}
		})
	}
}

func TestDockerModelRelayCancellationClosesUpstreamAndCleansWorker(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer upstream.Close()
	fixture := newDockerFixture(t)
	config := relayTestConfig(t, fixture, upstream)
	config.Timeout = 5 * time.Second
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, fixture.input()); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
	}
	waitForRunnerFile(t, filepath.Join(fixture.directory, "first-model-chunk"))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay cancellation hung")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream request survived cancellation")
	}
	commands := fixture.commands(t)
	if commands[len(commands)-1].Args[0] != "rm" {
		t.Fatal("canceled relay did not remove its Worker")
	}
}

func TestDockerModelRelayBoundsResponsesAndRejectsNonAPIContent(t *testing.T) {
	for _, test := range []struct {
		name, contentType string
		size              int
		want              error
	}{
		{"non API content", "text/html", 10, review.ErrExecutionFailed},
		{"response exceeds limit", "text/event-stream", (32 << 20) + 1, review.ErrOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", test.size)))
			}))
			defer upstream.Close()
			fixture := newDockerFixture(t)
			config := relayTestConfig(t, fixture, upstream)
			config.Timeout = 10 * time.Second
			runner, err := codex.NewDockerRunner(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(context.Background(), fixture.input())
			if !errors.Is(err, test.want) || report.SchemaVersion != "" {
				t.Fatalf("unexpected untrusted response: %v", err)
			}
		})
	}
}

func TestDockerModelRelayCancellationDoesNotWaitForEscapedClientPipeHolders(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("hanging Docker client made a model request") }))
	defer upstream.Close()
	fixture := newDockerFixture(t)
	config := relayTestConfig(t, fixture, upstream)
	config.Timeout = 10 * time.Second
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("pipe-child"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, fixture.input()); done <- err }()
	marker := filepath.Join(fixture.directory, "pipe-child-pid")
	finished := false
	t.Cleanup(func() {
		cancel()
		if content, err := os.ReadFile(marker); err == nil {
			if pid, err := strconv.Atoi(string(content)); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
		if !finished {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("relay did not finish after owned fixture cleanup")
			}
		}
	})
	waitForRunnerFile(t, marker)
	cancel()
	select {
	case err := <-done:
		finished = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay waited for an escaped Docker client stdout holder")
	}
}

func TestDockerModelRelayRefusesUnsupportedWorkersAndMissingDeploymentGateway(t *testing.T) {
	fixture := newDockerFixture(t)
	config := fixture.config()
	config.ModelRelay = true
	if _, err := codex.NewDockerRunner(context.Background(), config); !errors.Is(err, codex.ErrInvalidRunnerConfig) {
		t.Fatal("relay without gateway accepted: ", err)
	}
	config.Gateway = &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "review-service"}
	if err := os.WriteFile(filepath.Join(fixture.directory, "no-relay"), []byte("false"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.NewDockerRunner(context.Background(), config); !errors.Is(err, codex.ErrIncompatibleRuntime) {
		t.Fatal("old Worker accepted for relay: ", err)
	}
}

func TestDockerModelRelayRejectsTLSVerificationBypass(t *testing.T) {
	fixture := newDockerFixture(t)
	config := fixture.config()
	config.Gateway = &codex.GatewayConfig{BaseURL: "https://api.deepseek.com", ServicePrincipal: "review-service"}
	config.ModelRelay = true
	config.ModelHTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if _, err := codex.NewDockerRunner(context.Background(), config); !errors.Is(err, codex.ErrInvalidRunnerConfig) {
		t.Fatal("TLS verification bypass was accepted: ", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.directory, "commands.jsonl")); !os.IsNotExist(err) {
		t.Fatal("invalid TLS deployment reached Docker")
	}
}
