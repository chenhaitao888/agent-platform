package reference_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/connector/reference"
	"agent-platform/backend/internal/planning"
)

func TestReferenceReadersUseOnlyAuthorizedGETAndValidateReturnedScope(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-tenant", "wrong-resource", "redirect", "too-large", "secret", "duplicate", "wrong-version", "http-failure"} {
		t.Run(scenario, func(t *testing.T) {
			target := planning.Target{TenantID: "tenant-a", Environment: "test", ConnectorRef: "config-ref", ResourceRef: "shared/orders"}
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/config" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Query().Get("resourceRef") != target.ResourceRef || r.URL.Query().Get("tenantId") != target.TenantID || r.URL.Query().Get("environment") != "test" {
					t.Error("unexpected method, scope or credentials")
				}
				state := planning.ConfigState{Target: target, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{}}
				version := "config-read.v1"
				switch scenario {
				case "wrong-tenant":
					state.Target.TenantID = "another"
				case "wrong-resource":
					state.Target.ResourceRef = "another"
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(302)
					return
				case "too-large":
					_, _ = w.Write([]byte(strings.Repeat("x", (512<<10)+1)))
					return
				case "secret":
					state.Items = []planning.ConfigItem{{Key: "token", Version: "v1", Sensitive: true, Value: planning.Value{Type: "string", Text: "private-secret"}}}
				case "duplicate":
					_, _ = w.Write([]byte(`{"schemaVersion":"config-read.v1","schemaVersion":"config-read.v1","state":{}}`))
					return
				case "wrong-version":
					version = "config-read.v2"
				case "http-failure":
					w.WriteHeader(500)
					_, _ = w.Write([]byte("private-secret"))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": version, "state": state})
			}))
			defer server.Close()
			reader, err := reference.NewConfigReader(reference.Connection{ID: "config-ref", Version: "connection-v1", BaseURL: server.URL, Token: "fixture-token"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			value, err := reader.ReadConfig(context.Background(), target)
			if scenario == "success" {
				if err != nil || value.Target != target || reader.Describe().ConnectionVersion != "connection-v1" {
					t.Fatal("valid reference state rejected")
				}
			} else if err == nil || strings.Contains(err.Error(), "private") || len(value.Items) > 0 {
				t.Fatal("unsafe upstream state escaped")
			}
			if calls != 1 {
				t.Fatal("reader redirected or retried")
			}
		})
	}
}

func TestReferenceExperimentReaderPreservesInactiveDraftCapability(t *testing.T) {
	target := planning.Target{TenantID: "tenant", Environment: "test", ConnectorRef: "ab", ResourceRef: "app"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/experiment" {
			t.Error("unexpected request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": "experiment-read.v1", "state": planning.ExperimentState{Target: target, Version: "v1", DraftNoTraffic: false, ConditionalUpdates: true, Items: []planning.ExperimentItem{}, AllocationModes: []string{"weighted"}}})
	}))
	defer server.Close()
	reader, err := reference.NewExperimentReader(reference.Connection{ID: "ab", Version: "1", BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	state, err := reader.ReadExperiments(context.Background(), target)
	if err != nil || state.DraftNoTraffic {
		t.Fatal("adapter invented a draft capability")
	}
	for _, address := range []string{"http://localhost", "https://user:pass@example.invalid", "https://example.invalid?target=other", "https://example.invalid/#fragment"} {
		if _, err := reference.NewConfigReader(reference.Connection{ID: "config", Version: "1", BaseURL: address}, nil); err == nil {
			t.Fatal("unsafe connection accepted")
		}
	}
}
