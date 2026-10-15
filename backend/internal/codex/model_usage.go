package codex

import (
	"bytes"
	"encoding/json"
	"io"

	"agent-platform/backend/internal/review"
)

const maxUsageEventBytes = 1 << 20
const maxUsageTokens = 1_000_000_000_000

// One accumulator per RunWithUsage. It is never stored on the shared Runner.
type usageAccumulator struct{ usage review.ModelUsage }

func newUsageAccumulator() *usageAccumulator {
	zero := int64(0)
	return &usageAccumulator{usage: review.ModelUsage{CachedInputTokens: &zero, Complete: true}}
}

type responseUsage struct {
	Input   *int64 `json:"input_tokens"`
	Output  *int64 `json:"output_tokens"`
	Total   *int64 `json:"total_tokens"`
	Details *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// A bounded passive observer: bytes are forwarded immediately and parsing
// never changes model/Worker success or retains prompt, output or diagnostics.
type usageObserver struct {
	owner           *usageAccumulator
	stream          bool
	line, event     []byte
	lastCR, invalid bool
	terminals       int
	value           *responseUsage
}

func (a *usageAccumulator) observe(contentType string) *usageObserver {
	if a == nil {
		return nil
	}
	return &usageObserver{owner: a, stream: contentType == "text/event-stream"}
}

func (o *usageObserver) write(data []byte) {
	if o == nil || o.invalid {
		return
	}
	if !o.stream {
		if len(o.event)+len(data) > maxUsageEventBytes {
			o.invalid = true
			o.event = nil
			return
		}
		o.event = append(o.event, data...)
		return
	}
	for _, b := range data {
		if b == '\n' && o.lastCR {
			o.lastCR = false
			continue
		}
		o.lastCR = b == '\r'
		if b == '\r' || b == '\n' {
			o.consumeLine()
			o.line = o.line[:0]
			if o.invalid {
				o.event = nil
				o.line = nil
				return
			}
		} else {
			if len(o.line) >= maxUsageEventBytes {
				o.invalid = true
				o.line = nil
				o.event = nil
				return
			}
			o.line = append(o.line, b)
		}
	}
}

func (o *usageObserver) consumeLine() {
	if len(o.line) == 0 {
		if len(o.event) != 0 {
			o.consumeEvent(o.event)
			o.event = o.event[:0]
		}
		return
	}
	if bytes.Equal(o.line, []byte("data")) || bytes.HasPrefix(o.line, []byte("data:")) {
		data := []byte{}
		if len(o.line) > 4 {
			data = bytes.TrimPrefix(o.line[5:], []byte(" "))
		}
		if len(o.event)+len(data)+1 > maxUsageEventBytes {
			o.invalid = true
			return
		}
		o.event = append(o.event, data...)
		o.event = append(o.event, '\n')
	}
}

func (o *usageObserver) consumeEvent(data []byte) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return
	}
	fields, ok := usageObject(data)
	var kind string
	if !ok || json.Unmarshal(fields["type"], &kind) != nil {
		o.invalid = true
		return
	}
	switch kind {
	case "response.completed", "response.incomplete", "response.failed":
		o.consumeResponse(fields["response"])
	}
}

func (o *usageObserver) consumeResponse(data []byte) {
	o.terminals++
	fields, ok := usageObject(data)
	var status string
	if !ok || json.Unmarshal(fields["status"], &status) != nil || o.terminals != 1 {
		o.invalid = true
		return
	}
	if status != "completed" && status != "incomplete" && status != "failed" {
		o.invalid = true
		return
	}
	usageFields, ok := usageObject(fields["usage"])
	var u responseUsage
	if !ok || json.Unmarshal(usageFields["input_tokens"], &u.Input) != nil || json.Unmarshal(usageFields["output_tokens"], &u.Output) != nil || u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 || *u.Input > maxUsageTokens || *u.Output > maxUsageTokens {
		o.invalid = true
		return
	}
	if raw := usageFields["total_tokens"]; len(raw) != 0 && json.Unmarshal(raw, &u.Total) != nil {
		o.invalid = true
		return
	}
	if raw := usageFields["input_tokens_details"]; len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		details, ok := usageObject(raw)
		if !ok {
			o.invalid = true
			return
		}
		u.Details = &struct {
			Cached *int64 `json:"cached_tokens"`
		}{}
		if raw := details["cached_tokens"]; len(raw) != 0 && json.Unmarshal(raw, &u.Details.Cached) != nil {
			o.invalid = true
			return
		}
	}
	if u.Total != nil && *u.Total != *u.Input+*u.Output {
		o.invalid = true
		return
	}
	if u.Details != nil && u.Details.Cached != nil && (*u.Details.Cached < 0 || *u.Details.Cached > *u.Input) {
		o.invalid = true
		return
	}
	o.value = &u
}

// Reject duplicate keys in the objects that determine accounting. JSON's
// default last-key-wins behavior must not turn ambiguous counters into costs.
func usageObject(data []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	closing, err := d.Token()
	return fields, err == nil && closing == json.Delim('}') && d.Decode(new(any)) == io.EOF
}

func (o *usageObserver) finish(cleanEOF bool) {
	if o == nil {
		return
	}
	if !o.stream && !o.invalid {
		o.consumeResponse(o.event)
	}
	u := &o.owner.usage
	if !cleanEOF {
		u.Complete = false
	}
	if o.invalid || o.value == nil || o.terminals != 1 {
		u.Complete = false
		u.CachedInputTokens = nil
		return
	}
	u.MeasuredRequests++
	u.InputTokens += *o.value.Input
	u.OutputTokens += *o.value.Output
	u.TotalTokens = u.InputTokens + u.OutputTokens
	if o.value.Details == nil || o.value.Details.Cached == nil {
		u.CachedInputTokens = nil
	} else if u.CachedInputTokens != nil {
		*u.CachedInputTokens += *o.value.Details.Cached
	}
}
