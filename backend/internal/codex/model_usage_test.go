package codex_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agent-platform/backend/internal/codex"
)

func TestDockerRunnerReportsUsageFromHostResponse(t *testing.T) {
	fixture := newDockerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("usage"), 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Split the semantic event across writes and include an earlier usage
		// snapshot that must not be counted as another completed request.
		for _, part := range []string{
			"data: {\"type\":\"response.created\",\"response\":{\"usage\":{\"input_tokens\":100}}}\n\n",
			"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"us",
			"age\":{\"input_tokens\":100,\"output_tokens\":20,\"total_tokens\":120,\"input_tokens_details\":{\"cached_tokens\":40}}}}\r\n\r\n",
		} {
			_, _ = io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()
	runner, err := codex.NewDockerRunner(context.Background(), relayTestConfig(t, fixture, upstream))
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := runner.RunWithUsage(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || !usage.Complete || usage.Requests != 1 || usage.MeasuredRequests != 1 || usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.TotalTokens != 120 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 40 {
		t.Fatalf("incorrect host usage: %+v", usage)
	}
}

func TestUsageAccumulatesModelRoundsWithoutMixingConcurrentRuns(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		fixture := newDockerFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("usage-multiple"), 0o600); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 2 && failSecond {
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40}}}`)
		}))
		runner, err := codex.NewDockerRunner(context.Background(), relayTestConfig(t, fixture, upstream))
		if err != nil {
			upstream.Close()
			t.Fatal(err)
		}
		if failSecond {
			_, usage, err := runner.RunWithUsage(context.Background(), fixture.input())
			if err == nil || usage == nil || usage.Complete || usage.Requests != 2 || usage.MeasuredRequests != 1 || usage.TotalTokens != 120 {
				t.Errorf("failed second request hid consumed usage: %+v %v", usage, err)
			}
		} else {
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					_, usage, err := runner.RunWithUsage(context.Background(), fixture.input())
					if err != nil || usage == nil || !usage.Complete || usage.Requests != 2 || usage.MeasuredRequests != 2 || usage.TotalTokens != 240 {
						t.Errorf("concurrent invocation mixed telemetry: %+v %v", usage, err)
					}
				})
			}
			wg.Wait()
			if calls.Load() != 4 {
				t.Errorf("expected four model calls, got %d", calls.Load())
			}
		}
		upstream.Close()
	}
}

func TestUsageMissingMalformedOrRepeatedDoesNotBecomeZeroCost(t *testing.T) {
	valid := `{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":40}}}`
	event := func(response string) string {
		return "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
	}
	for _, c := range []struct {
		name, contentType, body string
		complete, cached        bool
	}{
		{"json", "application/json", valid, true, true},
		{"no-cache-detail", "application/json", `{"status":"completed","usage":{"input_tokens":100,"output_tokens":20}}`, true, false},
		{"missing", "text/event-stream", "data: [DONE]\n\n", false, false},
		{"negative", "application/json", strings.Replace(valid, `"input_tokens":100`, `"input_tokens":-1`, 1), false, false},
		{"fractional", "application/json", strings.Replace(valid, `"output_tokens":20`, `"output_tokens":1.5`, 1), false, false},
		{"null", "application/json", strings.Replace(valid, `"input_tokens":100`, `"input_tokens":null`, 1), false, false},
		{"overflow", "application/json", strings.Replace(valid, `"input_tokens":100`, `"input_tokens":9223372036854775808`, 1), false, false},
		{"wrong-total", "application/json", strings.Replace(valid, `"total_tokens":120`, `"total_tokens":1`, 1), false, false},
		{"cache-exceeds-input", "application/json", strings.Replace(valid, `"cached_tokens":40`, `"cached_tokens":101`, 1), false, false},
		{"duplicate-field", "application/json", strings.Replace(valid, `"input_tokens":100`, `"input_tokens":1,"input_tokens":100`, 1), false, false},
		{"repeated-terminal", "text/event-stream", event(valid) + event(valid), false, false},
		{"truncated-event", "text/event-stream", strings.TrimSuffix(event(valid), "\n\n"), false, false},
		{"oversized-event", "text/event-stream", "data: " + strings.Repeat("x", (1<<20)+1) + "\n\n" + event(valid), false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := newDockerFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("usage"), 0o600); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", c.contentType)
				_, _ = io.WriteString(w, c.body)
			}))
			defer upstream.Close()
			runner, err := codex.NewDockerRunner(context.Background(), relayTestConfig(t, fixture, upstream))
			if err != nil {
				t.Fatal(err)
			}
			report, usage, err := runner.RunWithUsage(context.Background(), fixture.input())
			if err != nil || report.SchemaVersion != "1.0" {
				t.Fatal("passive observer changed review outcome", err)
			}
			if usage == nil || usage.Requests != 1 || usage.Complete != c.complete || (usage.CachedInputTokens != nil) != c.cached {
				t.Fatalf("unexpected telemetry: %+v", usage)
			}
			if !c.complete && usage.MeasuredRequests != 0 {
				t.Fatal("untrusted usage counted")
			}
		})
	}
}
