package mcpbridge

import (
	"encoding/json"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/strictjson"
)

type Call struct {
	Tool           string `json:"tool"`
	ToolHash       string `json:"toolHash"`
	Arguments      any    `json:"arguments"`
	Result         any    `json:"result"`
	ResultEncoding string `json:"resultEncoding"` // structured or json-text
}
type Binding struct {
	SchemaVersion      string        `json:"schemaVersion"`
	Version            string        `json:"version"`
	SkillHash          string        `json:"skillHash"`
	ToolsDigest        string        `json:"toolsDigest"`
	CombinedSaveSubmit bool          `json:"combinedSaveSubmit"`
	Read               Call          `json:"read"`
	Save               Call          `json:"save"`
	LookupSave         Call          `json:"lookupSave"`
	Submit             *Call         `json:"submit,omitempty"`
	LookupSubmit       *Call         `json:"lookupSubmit,omitempty"`
	Ticket             Call          `json:"ticket"`
	Publication        Call          `json:"publication"`
	Published          Call          `json:"published"`
	Confirmation       *Confirmation `json:"confirmation,omitempty"`
}
type Confirmation struct {
	Digest string    `json:"digest"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

func (b Binding) Digest() string { b.Confirmation = nil; return planning.Hash(b) }
func (b Binding) Calls() []Call {
	calls := []Call{b.Read, b.Save, b.LookupSave, b.Ticket, b.Publication, b.Published}
	if b.Submit != nil {
		calls = append(calls, *b.Submit)
	}
	if b.LookupSubmit != nil {
		calls = append(calls, *b.LookupSubmit)
	}
	return calls
}
func (b Binding) Validate(confirmed bool) error {
	if b.SchemaVersion != "mcp-company-binding.v1" || !planning.ValidIdentifier(b.Version) || len(b.SkillHash) != 64 || len(b.ToolsDigest) != 64 {
		return ErrBinding
	}
	if b.CombinedSaveSubmit {
		if b.Submit != nil || b.LookupSubmit != nil {
			return ErrBinding
		}
	} else if b.Submit == nil || b.LookupSubmit == nil {
		return ErrBinding
	}
	for _, call := range b.Calls() {
		if strings.TrimSpace(call.Tool) == "" || len(call.ToolHash) != 64 || call.Arguments == nil || call.Result == nil || call.ResultEncoding != "structured" && call.ResultEncoding != "json-text" {
			return ErrBinding
		}
	}
	if confirmed && (b.Confirmation == nil || b.Confirmation.Digest != b.Digest() || strings.TrimSpace(b.Confirmation.By) == "" || b.Confirmation.At.IsZero()) {
		return ErrBinding
	}
	return nil
}
func (b Binding) CheckCatalog(c Catalog) error {
	for _, call := range b.Calls() {
		found := false
		for _, tool := range c.Tools {
			if tool.Name == call.Tool && tool.Hash() == call.ToolHash {
				found = true
				break
			}
		}
		if !found {
			return ErrBinding
		}
	}
	return nil
}
func strictDecode(data []byte, destination any) error {
	var check any
	if decodeJSON(data, &check) != nil || strictjson.Decode(data, destination) != nil {
		return ErrBinding
	}
	return nil
}
func ParseBinding(data []byte) (Binding, error) {
	var b Binding
	if strictDecode(data, &b) != nil {
		return b, ErrBinding
	}
	// Normalize all templates to ordinary JSON values, independent of caller.
	return b, b.Validate(false)
}
func scope(input any) map[string]any {
	data, _ := json.Marshal(input)
	var value any
	_ = decodeJSON(data, &value)
	return map[string]any{"input": value}
}
