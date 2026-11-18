package mcpbridge

import (
	"context"
	"slices"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

type Adapter struct {
	ReadClient  Client
	WriteClient Client
	Binding     Binding
	Version     string
	Targets     []planning.Target
}
type Reader struct{ Adapter *Adapter }

func (r Reader) Describe() planning.Descriptor {
	d := r.Adapter.Describe()
	d.ContractVersion = "config-read.v1"
	return d
}
func (r Reader) ReadConfig(ctx context.Context, t planning.Target) (planning.ConfigState, error) {
	return r.Adapter.ReadConfig(ctx, t)
}
func (a *Adapter) Describe() planning.Descriptor {
	return planning.Descriptor{Kind: "config", ContractVersion: "config-write.v1", ImplementationVersion: resourcewrite.CompanyImplementation, ConnectionVersion: a.Version}
}
func (a *Adapter) allowed(t planning.Target) bool {
	return slices.Contains(a.Targets, t) && a.Binding.Validate(true) == nil
}
func (a *Adapter) ReadConfig(ctx context.Context, t planning.Target) (planning.ConfigState, error) {
	var state planning.ConfigState
	if !a.allowed(t) {
		return state, ErrBinding
	}
	if a.ReadClient.call(ctx, a.Binding.Read, scope(map[string]any{"target": t}), &state) != nil || !planning.ValidConfigState(state, t) || state.SaveMode != "publish_required" || !state.ConditionalUpdates {
		return state, ErrBinding
	}
	return state, nil
}

// SaveResult is the normalized external result, before being sealed into the
// existing write receipt. Times/versions/request IDs must come from the server.
type SaveResult struct {
	ID              string                       `json:"id"`
	RequestID       string                       `json:"requestId"`
	Target          planning.Target              `json:"target"`
	Key             string                       `json:"key"`
	Status          string                       `json:"status"`
	Code            string                       `json:"code,omitempty"`
	BeforeVersion   string                       `json:"beforeVersion,omitempty"`
	ResourceVersion string                       `json:"resourceVersion,omitempty"`
	ItemVersion     string                       `json:"itemVersion,omitempty"`
	Value           *planning.Value              `json:"value,omitempty"`
	RecordedAt      time.Time                    `json:"recordedAt"`
	Change          *resourcewrite.CompanyChange `json:"change,omitempty"`
}

func (a *Adapter) save(ctx context.Context, i resourcewrite.Intent, lookup bool) (resourcewrite.Receipt, error) {
	var r resourcewrite.Receipt
	if !i.Valid() || !a.allowed(i.Target) || i.Writer != a.Describe() || i.Effect != "publish_required" {
		return r, resourcewrite.ErrInvalid
	}
	if !lookup && time.Now().After(i.ExpiresAt) {
		return r, resourcewrite.ErrExpired
	}
	r = resourcewrite.Receipt{SchemaVersion: "resource-write-receipt.v1", OperationID: i.OperationID, IntentHash: i.Hash, Kind: i.Kind, Target: i.Target, Key: i.Key}
	if i.Operation == "config.no_change" {
		state, err := a.ReadConfig(ctx, i.Target)
		if err != nil || state.Version != i.ResourceVersion {
			return r, resourcewrite.ErrUnknown
		}
		r.ID = i.OperationID
		r.RecordedAt = time.Now().UTC()
		if r.RecordedAt.After(i.ExpiresAt) {
			r.RecordedAt = i.ExpiresAt
		}
		r.Status = "APPLIED"
		r.BeforeResourceVersion = i.ResourceVersion
		r.ResourceVersion = i.ResourceVersion
		r.ItemVersion = i.ExpectedVersion
		r.ValueHash = i.ValueDigest()
		r.Effect = i.Effect
		v, err := resourcewrite.VerifyConfig(i, r, state)
		if err != nil || !v.ValidFor(i, r) {
			return r, resourcewrite.ErrUnknown
		}
		return r, nil
	}
	call, client := a.Binding.Save, a.WriteClient
	if lookup {
		call = a.Binding.LookupSave
		client = a.ReadClient
	}
	var saved SaveResult
	if err := client.call(ctx, call, scope(i), &saved); err != nil {
		return r, resourcewrite.ErrUnknown
	}
	if saved.RequestID != i.OperationID || saved.Target != i.Target || saved.Key != i.Key {
		return r, resourcewrite.ErrUnknown
	}
	r.ID = saved.ID
	r.Status = saved.Status
	r.Code = saved.Code
	r.RecordedAt = saved.RecordedAt
	if saved.Status == "APPLIED" {
		if saved.Change == nil || !planning.ValidIdentifier(saved.Change.ID) || !planning.ValidIdentifier(saved.Change.Version) || saved.Value == nil || !saved.Value.Valid() {
			return r, resourcewrite.ErrUnknown
		}
		r.Company = saved.Change
		r.BeforeResourceVersion = saved.BeforeVersion
		r.ResourceVersion = saved.ResourceVersion
		r.ItemVersion = saved.ItemVersion
		r.ValueHash = planning.Hash(saved.Value)
		r.Effect = i.Effect
		if a.Binding.CombinedSaveSubmit && (r.Company.Ticket == nil || !r.Company.Ticket.Matches(i, r, i.OperationID)) {
			return r, resourcewrite.ErrUnknown
		}
		if !a.Binding.CombinedSaveSubmit && r.Company.Ticket != nil {
			return r, resourcewrite.ErrUnknown
		}
	}
	if !r.ValidFor(i) {
		return r, resourcewrite.ErrUnknown
	}
	return r, nil
}
func (a *Adapter) Apply(ctx context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	return a.save(ctx, i, false)
}
func (a *Adapter) Lookup(ctx context.Context, i resourcewrite.Intent) (resourcewrite.Receipt, error) {
	return a.save(ctx, i, true)
}
func (a *Adapter) Verify(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt) (resourcewrite.Verification, error) {
	state, err := a.ReadConfig(ctx, i.Target)
	if err != nil {
		return resourcewrite.Verification{}, resourcewrite.ErrUnknown
	}
	return resourcewrite.VerifyConfig(i, r, state)
}
func ticketScope(i resourcewrite.Intent, r resourcewrite.Receipt, id string) map[string]any {
	return scope(map[string]any{"intent": i, "receipt": r, "requestId": id})
}
func (a *Adapter) ticket(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt, id string, lookup bool) (resourcewrite.CompanyTicket, error) {
	var ticket resourcewrite.CompanyTicket
	if !i.Valid() || !r.ValidFor(i) || !a.allowed(i.Target) || i.Writer != a.Describe() || r.Company == nil {
		return ticket, resourcewrite.ErrInvalid
	}
	if a.Binding.CombinedSaveSubmit {
		if r.Company.Ticket == nil {
			return ticket, resourcewrite.ErrUnknown
		}
		return *r.Company.Ticket, nil
	}
	call, client := a.Binding.Submit, a.WriteClient
	if lookup {
		call = a.Binding.LookupSubmit
		client = a.ReadClient
	} else if time.Now().After(i.ExpiresAt) {
		return ticket, resourcewrite.ErrExpired
	}
	if call == nil || client.call(ctx, *call, ticketScope(i, r, id), &ticket) != nil || !ticket.Matches(i, r, id) {
		return ticket, resourcewrite.ErrUnknown
	}
	return ticket, nil
}
func (a *Adapter) Submit(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt, id string) (resourcewrite.CompanyTicket, error) {
	return a.ticket(ctx, i, r, id, false)
}
func (a *Adapter) LookupTicket(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt, id string) (resourcewrite.CompanyTicket, error) {
	return a.ticket(ctx, i, r, id, true)
}

func (a *Adapter) Observe(ctx context.Context, i resourcewrite.Intent, r resourcewrite.Receipt, t *resourcewrite.CompanyTicket) (resourcewrite.CompanyObservation, error) {
	o := resourcewrite.CompanyObservation{ObservedAt: time.Now().UTC()}
	if !i.Valid() || !r.ValidFor(i) || !a.allowed(i.Target) || i.Writer != a.Describe() {
		return o, ErrBinding
	}
	input := map[string]any{"intent": i, "receipt": r}
	if i.Operation != "config.no_change" {
		if t == nil || !t.Matches(i, r, t.RequestID) {
			return o, ErrBinding
		}
		input["ticket"] = t
		var current resourcewrite.CompanyTicket
		if a.ReadClient.call(ctx, a.Binding.Ticket, scope(input), &current) != nil || current.ID != t.ID || !current.Matches(i, r, t.RequestID) {
			return o, ErrBinding
		}
		o.Ticket = &current
		switch current.State {
		case "PENDING":
			o.State = "WAITING_APPROVAL"
			return o, nil
		case "REJECTED", "WITHDRAWN", "EXPIRED":
			o.State = "APPROVAL_" + current.State
			return o, nil
		case "APPROVED":
		default:
			return o, ErrBinding
		}
		var publication resourcewrite.CompanyPublication
		if a.ReadClient.call(ctx, a.Binding.Publication, scope(input), &publication) != nil || publication.TicketID != t.ID || publication.ChangeID != t.ChangeID || publication.ChangeVersion != t.ChangeVersion || publication.Target != i.Target {
			return o, ErrBinding
		}
		o.Publication = &publication
		switch publication.State {
		case "PENDING":
			o.State = "WAITING_PUBLICATION"
			return o, nil
		case "PUBLISHING":
			o.State = "PUBLISHING"
			return o, nil
		case "FAILED":
			o.State = "PUBLICATION_FAILED"
			return o, nil
		case "PUBLISHED":
			if !planning.ValidIdentifier(publication.ID) || !planning.ValidIdentifier(publication.EffectiveVersion) {
				return o, ErrBinding
			}
		default:
			return o, ErrBinding
		}
		input["publication"] = publication
	}
	var active resourcewrite.PublishedConfig
	if a.ReadClient.call(ctx, a.Binding.Published, scope(input), &active) != nil || active.Target != i.Target || active.Key != i.Key || !active.Value.Valid() || !planning.ValidIdentifier(active.Version) {
		return o, ErrBinding
	}
	o.EffectiveVersion = active.Version
	o.ValueHash = planning.Hash(active.Value)
	if o.ValueHash != i.ValueDigest() || o.Publication != nil && active.Version != o.Publication.EffectiveVersion {
		o.State = "PUBLICATION_DRIFTED"
	} else {
		o.State = "PUBLISHED"
	}
	return o, nil
}
