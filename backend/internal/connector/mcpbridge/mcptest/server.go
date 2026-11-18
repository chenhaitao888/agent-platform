// Package mcptest is a controlled MCP provider for integration tests. It is not
// registered by any production executable and never talks to company services.
package mcptest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Provider struct {
	mu                                      sync.Mutex
	Server                                  *mcp.Server
	Prefix                                  string
	State                                   planning.ConfigState
	Combined                                bool
	Saves                                   map[string]mcpbridge.SaveResult
	Tickets                                 map[string]resourcewrite.CompanyTicket
	SaveCalls, SubmitCalls, Queries         int
	DropSave, DropSubmit                    bool
	Approval, Publication, EffectiveVersion string
	WrongChange, WrongValue                 bool
}

func New(prefix string, state planning.ConfigState, combined bool) *Provider {
	p := &Provider{Server: mcp.NewServer(&mcp.Implementation{Name: "controlled-company", Version: "1"}, nil), Prefix: prefix, State: state, Combined: combined, Saves: map[string]mcpbridge.SaveResult{}, Tickets: map[string]resourcewrite.CompanyTicket{}, Approval: "PENDING", Publication: "PENDING", EffectiveVersion: "published-v2"}
	for _, capability := range []string{"read", "save", "lookupSave", "submit", "lookupSubmit", "ticket", "publication", "published"} {
		p.Server.AddTool(&mcp.Tool{Name: prefix + capability, Description: "controlled " + capability, InputSchema: map[string]any{"type": "object"}}, p.handle(capability))
	}
	return p
}
func (p *Provider) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.Server }, &mcp.StreamableHTTPOptions{Stateless: true})
}
func (p *Provider) Counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.SaveCalls, p.SubmitCalls, p.Queries
}
func (p *Provider) Configure(approval, publication string, wrongChange, wrongValue bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Approval = approval
	p.Publication = publication
	p.WrongChange = wrongChange
	p.WrongValue = wrongValue
}
func (p *Provider) LoseResponse(save, submit bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.DropSave = save
	p.DropSubmit = submit
}
func (p *Provider) ReadState() planning.ConfigState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return planning.Clone(p.State)
}
func (p *Provider) handle(capability string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var i resourcewrite.Intent
		var envelope struct {
			Intent    resourcewrite.Intent        `json:"intent"`
			Receipt   resourcewrite.Receipt       `json:"receipt"`
			RequestID string                      `json:"requestId"`
			Ticket    resourcewrite.CompanyTicket `json:"ticket"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &i)
		_ = json.Unmarshal(req.Params.Arguments, &envelope)
		var out any
		switch capability {
		case "read":
			p.Queries++
			out = p.State
		case "save":
			p.SaveCalls++
			if !i.Valid() {
				return &mcp.CallToolResult{IsError: true}, nil
			}
			if saved, ok := p.Saves[i.OperationID]; ok {
				out = saved
				break
			}
			saved := mcpbridge.SaveResult{ID: "saved-" + i.OperationID, RequestID: i.OperationID, Target: i.Target, Key: i.Key, Status: "REJECTED", Code: "precondition_failed", RecordedAt: time.Now().UTC()}
			itemVersion := ""
			for _, item := range p.State.Items {
				if item.Key == i.Key {
					itemVersion = item.Version
				}
			}
			if saved.RecordedAt.After(i.ExpiresAt) {
				saved.Code = "intent_expired"
			} else if i.Target == p.State.Target && i.ResourceVersion == p.State.Version && (i.MustNotExist && itemVersion == "" || !i.MustNotExist && i.ExpectedVersion == itemVersion) {
				saved.Status = "APPLIED"
				saved.Code = ""
				saved.BeforeVersion = p.State.Version
				saved.ResourceVersion = fmt.Sprintf("v%d", len(p.Saves)+2)
				saved.ItemVersion = "item-" + saved.ResourceVersion
				saved.Value = i.Value
				saved.Change = &resourcewrite.CompanyChange{ID: "change-" + i.OperationID, Version: saved.ResourceVersion}
				p.State.Version = saved.ResourceVersion
				item := planning.ConfigItem{Key: i.Key, Version: saved.ItemVersion, Value: *i.Value}
				found := false
				for n := range p.State.Items {
					if p.State.Items[n].Key == i.Key {
						p.State.Items[n] = item
						found = true
					}
				}
				if !found {
					p.State.Items = append(p.State.Items, item)
				}
				if p.Combined {
					ticket := resourcewrite.CompanyTicket{ID: "order-" + i.OperationID, RequestID: i.OperationID, ChangeID: saved.Change.ID, ChangeVersion: saved.Change.Version, Target: i.Target, State: "PENDING", URL: "https://company.example/orders/" + i.OperationID}
					p.Tickets[ticket.RequestID] = ticket
					saved.Change.Ticket = &ticket
				}
			}
			p.Saves[i.OperationID] = saved
			out = saved
			if p.DropSave {
				p.DropSave = false
				return &mcp.CallToolResult{IsError: true}, nil
			}
		case "lookupSave":
			p.Queries++
			saved, ok := p.Saves[i.OperationID]
			if !ok {
				return &mcp.CallToolResult{IsError: true}, nil
			}
			out = saved
		case "submit":
			p.SubmitCalls++
			if envelope.Receipt.Company == nil || time.Now().After(envelope.Intent.ExpiresAt) {
				return &mcp.CallToolResult{IsError: true}, nil
			}
			ticket, ok := p.Tickets[envelope.RequestID]
			if !ok {
				ticket = resourcewrite.CompanyTicket{ID: "order-" + envelope.RequestID, RequestID: envelope.RequestID, ChangeID: envelope.Receipt.Company.ID, ChangeVersion: envelope.Receipt.Company.Version, Target: envelope.Intent.Target, State: "PENDING", URL: "https://company.example/orders/" + envelope.RequestID}
				p.Tickets[envelope.RequestID] = ticket
			}
			out = ticket
			if p.DropSubmit {
				p.DropSubmit = false
				return &mcp.CallToolResult{IsError: true}, nil
			}
		case "lookupSubmit":
			p.Queries++
			ticket, ok := p.Tickets[envelope.RequestID]
			if !ok {
				return &mcp.CallToolResult{IsError: true}, nil
			}
			out = ticket
		case "ticket":
			p.Queries++
			ticket, ok := p.Tickets[envelope.Ticket.RequestID]
			if !ok {
				return &mcp.CallToolResult{IsError: true}, nil
			}
			ticket.State = p.Approval
			if p.WrongChange {
				ticket.ChangeVersion = "someone-else"
			}
			out = ticket
		case "publication":
			p.Queries++
			t := envelope.Ticket
			out = resourcewrite.CompanyPublication{ID: "publication-" + t.ID, TicketID: t.ID, ChangeID: t.ChangeID, ChangeVersion: t.ChangeVersion, Target: t.Target, State: p.Publication, EffectiveVersion: p.EffectiveVersion}
		case "published":
			p.Queries++
			value := *envelope.Intent.Value
			if p.WrongValue {
				value = planning.Value{Type: "string", Text: "outside-change"}
			}
			out = resourcewrite.PublishedConfig{Target: envelope.Intent.Target, Key: envelope.Intent.Key, Version: p.EffectiveVersion, Value: value}
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{"payload": out}}, nil
	}
}
func (p *Provider) Binding(c mcpbridge.Catalog) mcpbridge.Binding {
	call := func(capability string) mcpbridge.Call {
		for _, t := range c.Tools {
			if t.Name == p.Prefix+capability {
				return mcpbridge.Call{Tool: t.Name, ToolHash: t.Hash(), Arguments: map[string]any{"$path": "/input"}, Result: map[string]any{"$path": "/result/payload"}, ResultEncoding: "structured"}
			}
		}
		panic("missing fixture tool")
	}
	b := mcpbridge.Binding{SchemaVersion: "mcp-company-binding.v1", Version: "v1", SkillHash: planning.Hash("company fixture"), ToolsDigest: c.Digest, CombinedSaveSubmit: p.Combined, Read: call("read"), Save: call("save"), LookupSave: call("lookupSave"), Ticket: call("ticket"), Publication: call("publication"), Published: call("published")}
	if !p.Combined {
		submit, lookup := call("submit"), call("lookupSubmit")
		b.Submit = &submit
		b.LookupSubmit = &lookup
	}
	b.Confirmation = &mcpbridge.Confirmation{Digest: b.Digest(), By: "fixture-owner", At: time.Now().UTC()}
	return b
}
