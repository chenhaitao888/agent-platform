package mcpbridge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/connector/mcpbridge"
)

func TestModelOnlyProposesBindingAndCannotConfirmOrInventTools(t *testing.T) {
	for _, mode := range []string{"valid", "questions", "confirmed", "invented-tool", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			a, p, _ := fixture(t, false)
			catalog, err := a.ReadClient.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			proposal := mcpbridge.Proposal{Binding: a.Binding, Questions: []string{}}
			proposal.Binding.Confirmation = nil
			switch mode {
			case "questions":
				proposal.Questions = []string{"公司工单状态字段含义？"}
			case "confirmed":
				proposal.Binding.Confirmation = a.Binding.Confirmation
			case "invented-tool":
				proposal.Binding.Save.Tool = "approve_and_publish"
			}
			body, _ := json.Marshal(proposal)
			if mode == "malformed" {
				body = []byte(`{"binding":{},"questions":[],"questions":["hidden"]}`)
			}
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				input, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer model-only" || strings.Contains(string(input), "writeCredentialFile") || strings.Contains(string(input), "readCredentialFile") {
					t.Error("model request boundary")
				}
				var payload map[string]any
				if json.Unmarshal(input, &payload) != nil || payload["store"] != false || payload["tools"] != nil {
					t.Error("model has tool execution or storage")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": string(body)}}}}})
			}))
			defer model.Close()
			result, err := mcpbridge.Suggest(context.Background(), model.Client(), model.URL, "model-only", "fixture-model", "company fixture", catalog)
			if mode == "valid" || mode == "questions" {
				if err != nil {
					t.Fatal(err)
				}
				if result.Binding.Confirmation != nil {
					t.Fatal("model confirmed binding")
				}
			} else if err == nil {
				t.Fatal("unsafe model proposal accepted")
			}
			saves, submits, _ := p.Counts()
			if saves != 0 || submits != 0 {
				t.Fatal("suggestion invoked company tool")
			}
		})
	}
}
