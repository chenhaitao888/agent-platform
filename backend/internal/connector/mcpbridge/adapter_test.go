package mcpbridge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/connector/mcpbridge/mcptest"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixture(t *testing.T, combined bool) (*mcpbridge.Adapter, *mcptest.Provider, resourcewrite.Intent) {
	t.Helper()
	target := planning.Target{TenantID: "tenant", Environment: "test", ConnectorRef: "company", ResourceRef: "app/space"}
	p := mcptest.New("custom_", planning.ConfigState{Target: target, Version: "v1", SaveMode: "publish_required", ConditionalUpdates: true, Items: []planning.ConfigItem{}}, combined)
	s := httptest.NewServer(p.Handler())
	t.Cleanup(s.Close)
	c := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "streamable-http", URL: s.URL}}
	catalog, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := &mcpbridge.Adapter{ReadClient: c, WriteClient: c, Binding: p.Binding(catalog), Version: "v1", Targets: []planning.Target{target}}
	i := resourcewrite.Intent{SchemaVersion: "resource-write.v1", OperationID: "op-1", PlanHash: strings.Repeat("a", 64), ActionID: "action-1", ActionHash: strings.Repeat("b", 64), Kind: "config", Target: target, Key: "feature.enabled", Operation: "config.create", Effect: "publish_required", Reader: (mcpbridge.Reader{Adapter: a}).Describe(), Writer: a.Describe(), OriginalResourceVersion: "v1", ResourceVersion: "v1", MustNotExist: true, Value: &planning.Value{Type: "boolean", Text: "true"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	i.Hash = i.Digest()
	return a, p, i
}

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_MCP_TEST_HELPER") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "stdio-fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "company_query", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
	})
	if s.Run(context.Background(), &mcp.StdioTransport{}) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func TestMCPStdioAndLegacySSEDiscovery(t *testing.T) {
	t.Run("stdio", func(t *testing.T) {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		client := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "stdio", Command: binary, Args: []string{"-test.run=^TestMCPStdioHelper$"}, Directory: t.TempDir()}, Credentials: mcpbridge.Credentials{Env: map[string]string{"AGENT_PLATFORM_MCP_TEST_HELPER": "1"}}}
		catalog, err := client.Discover(context.Background())
		if err != nil || len(catalog.Tools) != 1 {
			t.Fatal("stdio discovery", err)
		}
	})
	t.Run("sse", func(t *testing.T) {
		_, p, _ := fixture(t, false)
		server := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return p.Server }, nil))
		defer server.Close()
		client := mcpbridge.Client{Connection: mcpbridge.Connection{Transport: "sse", URL: server.URL}}
		catalog, err := client.Discover(context.Background())
		if err != nil || len(catalog.Tools) != 8 {
			t.Fatal("SSE discovery", err)
		}
	})
}
func TestConfigurableMCPToolsSaveSubmitAndVerifyAutomaticPublication(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "combined"}[combined], func(t *testing.T) {
			a, p, i := fixture(t, combined)
			ctx := context.Background()
			state, err := a.ReadConfig(ctx, i.Target)
			if err != nil || state.Version != "v1" {
				t.Fatal("read", err)
			}
			r, err := a.Apply(ctx, i)
			if err != nil || !r.ValidFor(i) {
				t.Fatal("save", err)
			}
			v, err := a.Verify(ctx, i, r)
			if err != nil || !v.ValidFor(i, r) {
				t.Fatal("verify save", err)
			}
			requestID := "ticket-1"
			if combined {
				requestID = i.OperationID
			}
			ticket, err := a.Submit(ctx, i, r, requestID)
			if err != nil {
				t.Fatal("submit", err)
			}
			for _, test := range []struct{ approval, publication, want string }{{"PENDING", "PENDING", "WAITING_APPROVAL"}, {"APPROVED", "PENDING", "WAITING_PUBLICATION"}, {"APPROVED", "PUBLISHING", "PUBLISHING"}, {"APPROVED", "PUBLISHED", "PUBLISHED"}, {"REJECTED", "PUBLISHED", "APPROVAL_REJECTED"}, {"WITHDRAWN", "PUBLISHED", "APPROVAL_WITHDRAWN"}, {"EXPIRED", "PUBLISHED", "APPROVAL_EXPIRED"}, {"APPROVED", "FAILED", "PUBLICATION_FAILED"}} {
				p.Configure(test.approval, test.publication, false, false)
				observed, err := a.Observe(ctx, i, r, &ticket)
				if err != nil || observed.State != test.want {
					t.Fatalf("observe %s: %+v %v", test.want, observed, err)
				}
			}
			p.Configure("APPROVED", "PUBLISHED", true, false)
			if _, err := a.Observe(ctx, i, r, &ticket); err == nil {
				t.Fatal("accepted approval for another change")
			}
			p.Configure("APPROVED", "PUBLISHED", false, true)
			observed, err := a.Observe(ctx, i, r, &ticket)
			if err != nil || observed.State != "PUBLICATION_DRIFTED" {
				t.Fatal("missed value drift")
			}
			saves, submits, _ := p.Counts()
			want := 1
			if combined {
				want = 0
			}
			if saves != 1 || submits != want {
				t.Fatalf("effects %d/%d", saves, submits)
			}
		})
	}
}
func TestMCPDroppedResponsesOnlyRecoverOriginalResults(t *testing.T) {
	a, p, i := fixture(t, false)
	ctx := context.Background()
	p.LoseResponse(true, false)
	if _, err := a.Apply(ctx, i); err == nil {
		t.Fatal("lost save became success")
	}
	r, err := a.Lookup(ctx, i)
	if err != nil || !r.ValidFor(i) {
		t.Fatal("save lookup", err)
	}
	p.LoseResponse(false, true)
	if _, err := a.Submit(ctx, i, r, "ticket-1"); err == nil {
		t.Fatal("lost submission became success")
	}
	ticket, err := a.LookupTicket(ctx, i, r, "ticket-1")
	if err != nil || !ticket.Matches(i, r, "ticket-1") {
		t.Fatal("ticket lookup", err)
	}
	saves, submits, _ := p.Counts()
	if saves != 1 || submits != 1 {
		t.Fatal("adapter retried effects")
	}
}
func TestMCPChangedToolsUnconfirmedBindingsAndTargetsBlockBeforeWrites(t *testing.T) {
	for _, mode := range []string{"schema", "confirmation", "target"} {
		t.Run(mode, func(t *testing.T) {
			a, p, i := fixture(t, false)
			switch mode {
			case "schema":
				p.Server.AddTool(&mcp.Tool{Name: p.Prefix + "save", Description: "changed behavior", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					t.Error("called changed tool")
					return nil, nil
				})
			case "confirmation":
				a.Binding.Confirmation = nil
			case "target":
				i.Target.Environment = "prod"
				i.Hash = i.Digest()
			}
			if _, err := a.Apply(context.Background(), i); err == nil {
				t.Fatal("accepted invalid binding")
			}
			saves, _, _ := p.Counts()
			if saves != 0 {
				t.Fatal("unexpected mutation")
			}
		})
	}
}

func TestMCPDoesNotReplayToolToAnswerServerInputRequests(t *testing.T) {
	a, p, i := fixture(t, false)
	var calls atomic.Int32
	p.Server.AddTool(&mcp.Tool{Name: p.Prefix + "save", Description: "requires client input", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"form": &mcp.ElicitParams{Mode: "form", Message: "request another execution", RequestedSchema: map[string]any{"type": "object"}}}, RequestState: "opaque-state"}, nil
	})
	catalog, err := a.ReadClient.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.Binding = p.Binding(catalog)
	if _, err := a.Apply(context.Background(), i); err == nil {
		t.Fatal("input-required response accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("client replayed tool: %d", calls.Load())
	}
}
func TestTemplateEnumsArraysAndCreateUpdateCases(t *testing.T) {
	scope := map[string]any{"input": map[string]any{"operation": "config.create"}, "result": map[string]any{"status": "approved", "items": []any{map[string]any{"id": float64(7)}}}}
	template := map[string]any{"state": map[string]any{"$path": "/result/status", "$map": map[string]any{"approved": "APPROVED"}}, "items": map[string]any{"$each": "/result/items", "template": map[string]any{"id": map[string]any{"$path": "/item/id", "$as": "string"}}}, "precondition": map[string]any{"$switch": "/input/operation", "cases": map[string]any{"config.create": map[string]any{"absent": true}, "config.update": map[string]any{"$path": "/input/expectedVersion"}}}}
	out, err := mcpbridge.Render(template, scope)
	if err != nil || !reflect.DeepEqual(out, map[string]any{"state": "APPROVED", "items": []any{map[string]any{"id": "7"}}, "precondition": map[string]any{"absent": true}}) {
		t.Fatal(err)
	}
	scope["result"].(map[string]any)["status"] = "new_unknown_status"
	if _, err := mcpbridge.Render(template, scope); err == nil {
		t.Fatal("unknown enum did not fail closed")
	}
}
