package delivery

import (
	"context"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

// WritePolicy is supplied by trusted deployment code, never by a browser/model.
// Authorize must check current subject, tenant, service, environment, repository
// and resource bindings, and return a stamp including all permission/config versions.
// Operations are approve_write, execute_write and revoke_write. Revalidate must
// recheck frozen repository refs and resource conditions adjusted ONLY by prefix.
type WritePolicy interface {
	Authorize(context.Context, string, Bundle, string) (string, error)
	Revalidate(context.Context, Bundle, []resourcewrite.Completed) error
	Writer(context.Context, string, planning.Target) (resourcewrite.Writer, error)
}

type WriteBinding struct {
	ActionID   string              `json:"actionId"`
	ActionHash string              `json:"actionHash"`
	Kind       string              `json:"kind"`
	Target     planning.Target     `json:"target"`
	Writer     planning.Descriptor `json:"writer"`
}
type WriteApproval struct {
	Approval
	Bindings []WriteBinding `json:"bindings"`
}
type WriteStartInput struct {
	TenantID, BundleID, Subject string
	IdempotencyKey              string `json:"idempotencyKey"`
	ExpectedDigest              string `json:"expectedDigest"`
	ApprovalID                  string `json:"approvalId"`
}
type WriteExecution struct {
	ID          string           `json:"id"`
	TenantID    string           `json:"tenantId"`
	BundleID    string           `json:"bundleId"`
	ApprovalID  string           `json:"approvalId"`
	Subject     string           `json:"subject"`
	PolicyHash  string           `json:"policyHash"`
	Mode        string           `json:"mode"`
	State       string           `json:"state"`
	ErrorCode   string           `json:"errorCode,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
	CompletedAt *time.Time       `json:"completedAt,omitempty"`
	WorkflowID  string           `json:"workflowId"`
	Operations  []WriteOperation `json:"operations"`
}
type WriteOperation struct {
	ID           string                      `json:"id"`
	ActionID     string                      `json:"actionId"`
	ActionHash   string                      `json:"actionHash"`
	State        string                      `json:"state"`
	ErrorCode    string                      `json:"errorCode,omitempty"`
	Intent       *resourcewrite.Intent       `json:"intent,omitempty"`
	Receipt      *resourcewrite.Receipt      `json:"receipt,omitempty"`
	Verification *resourcewrite.Verification `json:"verification,omitempty"`
	DispatchedAt *time.Time                  `json:"dispatchedAt,omitempty"`
	LookupAfter  *time.Time                  `json:"lookupAfter,omitempty"`
}

// No production assembly exists yet. Only trusted callers may use this service;
// a future HTTP adapter must authorize reads/reconciliation as well as mutations.
type WriteService struct {
	Store             *Store
	Policy            WritePolicy
	AllowSelfApproval bool
}

func writePrefix(r WriteExecution, ordinal int) ([]resourcewrite.Completed, error) {
	out := make([]resourcewrite.Completed, 0, ordinal)
	for _, o := range r.Operations[:ordinal] {
		if o.State != "VERIFIED" || o.Intent == nil || o.Receipt == nil || o.Verification == nil || !o.Verification.ValidFor(*o.Intent, *o.Receipt) {
			return nil, ErrBusy
		}
		out = append(out, resourcewrite.Completed{Intent: *o.Intent, Receipt: *o.Receipt, Verification: *o.Verification})
	}
	return out, nil
}
func writeOrdinal(r WriteExecution, id string) (int, error) {
	for n, o := range r.Operations {
		if o.ID == id {
			return n, nil
		}
	}
	return 0, ErrNotFound
}
func activeWriteApproval(a WriteApproval, run string, now time.Time) bool {
	return a.Mode == "WRITE" && now.Before(a.ExpiresAt) && (a.State == "APPROVED" && run == "" || a.State == "CONSUMED" && a.ConsumedBy == run && run != "")
}
