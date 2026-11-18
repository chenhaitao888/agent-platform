// Package resourcewrite defines external write intents and verifiable outcomes.
// An intent is not an approval. The delivery control plane must authorize and
// durably record it before a Writer is invoked; no business API exposes it yet.
package resourcewrite

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"agent-platform/backend/internal/planning"
)

var (
	ErrInvalid  = errors.New("invalid resource write intent")
	ErrUnknown  = errors.New("resource write outcome is unknown")
	ErrConflict = errors.New("operation ID is bound to another intent")
	ErrExpired  = errors.New("resource write intent expired")
)

type Intent struct {
	SchemaVersion           string                   `json:"schemaVersion"`
	OperationID             string                   `json:"operationId"`
	PlanHash                string                   `json:"planHash"`
	ActionID                string                   `json:"actionId"`
	ActionHash              string                   `json:"actionHash"`
	Kind                    string                   `json:"kind"`
	Target                  planning.Target          `json:"target"`
	Key                     string                   `json:"key"`
	Operation               string                   `json:"operation"`
	Effect                  string                   `json:"effect"`
	Reader                  planning.Descriptor      `json:"reader"`
	Writer                  planning.Descriptor      `json:"writer"`
	OriginalResourceVersion string                   `json:"originalResourceVersion"`
	ResourceVersion         string                   `json:"resourceVersion"`
	ExpectedVersion         string                   `json:"expectedVersion,omitempty"`
	MustNotExist            bool                     `json:"mustNotExist"`
	Value                   *planning.Value          `json:"value,omitempty"`
	Experiment              *planning.ExperimentSpec `json:"experiment,omitempty"`
	ExpiresAt               time.Time                `json:"expiresAt"`
	Hash                    string                   `json:"hash"`
}

// Receipt is immutable provider evidence. It contains digests, never raw values.
// Both APPLIED and REJECTED receipts must be stored atomically with the decision.
type Receipt struct {
	Company               *CompanyChange  `json:"company,omitempty"`
	SchemaVersion         string          `json:"schemaVersion"`
	ID                    string          `json:"id"`
	OperationID           string          `json:"operationId"`
	IntentHash            string          `json:"intentHash"`
	Kind                  string          `json:"kind"`
	Target                planning.Target `json:"target"`
	Key                   string          `json:"key"`
	Status                string          `json:"status"`
	Code                  string          `json:"code,omitempty"`
	BeforeResourceVersion string          `json:"beforeResourceVersion,omitempty"`
	ResourceVersion       string          `json:"resourceVersion,omitempty"`
	ItemVersion           string          `json:"itemVersion,omitempty"`
	ValueHash             string          `json:"valueHash,omitempty"`
	Effect                string          `json:"effect,omitempty"`
	RecordedAt            time.Time       `json:"recordedAt"`
}
type Verification struct {
	OperationID     string    `json:"operationId"`
	IntentHash      string    `json:"intentHash"`
	ReceiptID       string    `json:"receiptId"`
	State           string    `json:"state"`
	Code            string    `json:"code,omitempty"`
	ResourceVersion string    `json:"resourceVersion,omitempty"`
	ItemVersion     string    `json:"itemVersion,omitempty"`
	ObservedAt      time.Time `json:"observedAt"`
}
type Completed struct {
	Intent       Intent       `json:"intent"`
	Receipt      Receipt      `json:"receipt"`
	Verification Verification `json:"verification"`
}

type Writer interface {
	Describe() planning.Descriptor
	// Apply makes one attempt. Neither errors nor timeouts imply non-execution.
	Apply(context.Context, Intent) (Receipt, error)
	// Lookup returning ErrUnknown (including a 404) never proves non-execution.
	Lookup(context.Context, Intent) (Receipt, error)
	// Verify reads the actual resource; a receipt alone does not establish current state.
	Verify(context.Context, Intent, Receipt) (Verification, error)
}

var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validDescriptor(d planning.Descriptor, kind, contract string) bool {
	return d.Kind == kind && d.ContractVersion == kind+"-"+contract+".v1" && planning.ValidIdentifier(d.ImplementationVersion) && planning.ValidIdentifier(d.ConnectionVersion)
}
func ValidTarget(t planning.Target) bool {
	return planning.ValidIdentifier(t.TenantID) && planning.ValidIdentifier(t.Environment) && planning.ValidIdentifier(t.ConnectorRef) && planning.ValidIdentifier(t.ResourceRef)
}
func (i Intent) Digest() string { i.Hash = ""; return planning.Hash(i) }
func (i Intent) ValueDigest() string {
	if i.Kind == "config" {
		return planning.Hash(i.Value)
	}
	return planning.Hash(i.Experiment)
}
func (i Intent) Valid() bool {
	if i.SchemaVersion != "resource-write.v1" || !planning.ValidIdentifier(i.OperationID) || !digest.MatchString(i.PlanHash) || !planning.ValidIdentifier(i.ActionID) || !digest.MatchString(i.ActionHash) || !ValidTarget(i.Target) || !planning.ValidIdentifier(i.Key) || !planning.ValidIdentifier(i.OriginalResourceVersion) || !planning.ValidIdentifier(i.ResourceVersion) || !validDescriptor(i.Reader, i.Kind, "read") || !validDescriptor(i.Writer, i.Kind, "write") || i.Reader.ConnectionVersion != i.Writer.ConnectionVersion || i.ExpiresAt.IsZero() || i.ExpiresAt.Year() < 1 || i.ExpiresAt.Year() > 9999 || i.Hash != i.Digest() {
		return false
	}
	if i.MustNotExist {
		if i.ExpectedVersion != "" {
			return false
		}
	} else if !planning.ValidIdentifier(i.ExpectedVersion) {
		return false
	}
	switch i.Kind {
	case "config":
		if i.Value == nil || !i.Value.Valid() || i.Experiment != nil || !slices.Contains([]string{"draft", "publish_required", "immediate"}, i.Effect) {
			return false
		}
		if i.Operation == "config.create" {
			return i.MustNotExist
		}
		return !i.MustNotExist && slices.Contains([]string{"config.update", "config.no_change"}, i.Operation)
	case "experiment":
		if i.Experiment == nil || !i.Experiment.Valid() || i.Value != nil || i.Effect != "inactive_draft" {
			return false
		}
		if i.Operation == "experiment.create_draft" {
			return i.MustNotExist
		}
		return i.Operation == "experiment.update_draft" && !i.MustNotExist
	}
	return false
}
func (r Receipt) ValidFor(i Intent) bool {
	if !i.Valid() || r.SchemaVersion != "resource-write-receipt.v1" || !planning.ValidIdentifier(r.ID) || r.OperationID != i.OperationID || r.IntentHash != i.Hash || r.Kind != i.Kind || r.Target != i.Target || r.Key != i.Key || r.RecordedAt.IsZero() {
		return false
	}
	if r.Status == "REJECTED" {
		return r.Company == nil && slices.Contains([]string{"precondition_failed", "intent_expired", "unsupported", "value_rejected"}, r.Code) && r.BeforeResourceVersion == "" && r.ResourceVersion == "" && r.ItemVersion == "" && r.ValueHash == "" && r.Effect == ""
	}
	if r.Status != "APPLIED" || r.Code != "" || r.RecordedAt.After(i.ExpiresAt) || r.BeforeResourceVersion != i.ResourceVersion || !planning.ValidIdentifier(r.ResourceVersion) || !planning.ValidIdentifier(r.ItemVersion) || r.ValueHash != i.ValueDigest() || r.Effect != i.Effect {
		return false
	}
	if i.Operation == "config.no_change" {
		return r.ResourceVersion == i.ResourceVersion && r.ItemVersion == i.ExpectedVersion
	}
	if i.Writer.ImplementationVersion == CompanyImplementation && (r.Company == nil || !planning.ValidIdentifier(r.Company.ID) || !planning.ValidIdentifier(r.Company.Version)) {
		return false
	}
	return r.ResourceVersion != i.ResourceVersion && (i.MustNotExist || r.ItemVersion != i.ExpectedVersion)
}
func (v Verification) ValidFor(i Intent, r Receipt) bool {
	return r.ValidFor(i) && r.Status == "APPLIED" && v.OperationID == i.OperationID && v.IntentHash == i.Hash && v.ReceiptID == r.ID && v.State == "VERIFIED" && v.Code == "" && v.ResourceVersion == r.ResourceVersion && v.ItemVersion == r.ItemVersion && !v.ObservedAt.IsZero() && !v.ObservedAt.Before(r.RecordedAt)
}

// Prepare derives a single intent from a sealed plan and the verified prefix of
// its execution. Only this run's receipts may advance a shared resource version.
// Callers persist the result once; retrying must reuse that exact intent/hash.
func Prepare(plan planning.Plan, ordinal int, operationID string, writer planning.Descriptor, expiresAt time.Time, completed []Completed) (Intent, error) {
	copy := plan
	copy.Hash = ""
	if plan.SchemaVersion != "change-plan.v1" || plan.State != "READY_FOR_REVIEW" || len(plan.Issues) != 0 || plan.Hash != planning.Hash(copy) || !digest.MatchString(plan.Hash) || ordinal < 0 || ordinal >= len(plan.Actions) || len(completed) != ordinal {
		return Intent{}, ErrInvalid
	}
	seen := map[string]bool{}
	versions := map[string]string{}
	baselines := map[string]string{}
	for n := 0; n <= ordinal; n++ {
		a := plan.Actions[n]
		action := a
		action.Hash = ""
		if a.Hash != planning.Hash(action) || a.Target.TenantID != plan.TenantID || a.Target.Environment != plan.Environment || len(a.Proposals) == 0 || seen["id:"+a.ID] {
			return Intent{}, ErrInvalid
		}
		seen["id:"+a.ID] = true
		resource := planning.Hash(struct {
			Kind   string
			Target planning.Target
		}{a.Kind, a.Target})
		baseline := planning.Hash(struct {
			Version    string
			Descriptor planning.Descriptor
		}{a.ResourceVersion, a.Descriptor})
		if old, ok := baselines[resource]; ok && old != baseline {
			return Intent{}, ErrInvalid
		}
		baselines[resource] = baseline
		if seen[resource+":"+a.Key] {
			return Intent{}, ErrInvalid
		}
		seen[resource+":"+a.Key] = true
		value := a.Proposals[0]
		for _, p := range a.Proposals {
			if planning.Hash(p.Value) != planning.Hash(value.Value) || planning.Hash(p.Experiment) != planning.Hash(value.Experiment) {
				return Intent{}, ErrInvalid
			}
		}
		version := a.ResourceVersion
		if prior, ok := versions[resource]; ok {
			version = prior
		}
		id, desc, expiry := operationID, writer, expiresAt
		if n < ordinal {
			id = completed[n].Intent.OperationID
			desc = completed[n].Intent.Writer
			expiry = completed[n].Intent.ExpiresAt
		}
		i := Intent{SchemaVersion: "resource-write.v1", OperationID: id, PlanHash: plan.Hash, ActionID: a.ID, ActionHash: a.Hash, Kind: a.Kind, Target: a.Target, Key: a.Key, Operation: a.Operation, Effect: a.Effect, Reader: a.Descriptor, Writer: desc, OriginalResourceVersion: a.ResourceVersion, ResourceVersion: version, ExpectedVersion: a.ExpectedVersion, MustNotExist: a.MustNotExist, Value: planning.Clone(value.Value), Experiment: planning.Clone(value.Experiment), ExpiresAt: expiry.UTC()}
		i.Hash = i.Digest()
		if !i.Valid() || seen["op:"+id] {
			return Intent{}, ErrInvalid
		}
		seen["op:"+id] = true
		if n == ordinal {
			return i, nil
		}
		prior := completed[n]
		if planning.Hash(i) != planning.Hash(prior.Intent) || !prior.Verification.ValidFor(i, prior.Receipt) {
			return Intent{}, ErrInvalid
		}
		versions[resource] = prior.Receipt.ResourceVersion
	}
	return Intent{}, ErrInvalid
}
