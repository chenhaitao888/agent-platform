package delivery

import (
	"context"
	"errors"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
)

var (
	ErrUnready   = errors.New("delivery is not ready for simulation approval")
	ErrApproval  = errors.New("simulation approval is inactive, expired or already used")
	ErrVersion   = errors.New("approval version conflict")
	ErrBusy      = errors.New("simulation operation is leased")
	ErrLeaseLost = errors.New("simulation lease lost")
	ErrRetry     = errors.New("simulation outcome requires reconciliation")
	ErrPolicy    = errors.New("simulation permission or binding changed")
	ErrDrift     = errors.New("frozen repository or resource state changed")
)

type ApprovalInput struct {
	TenantID, BundleID, Subject, PolicyHash string
	IdempotencyKey                          string `json:"idempotencyKey"`
	ExpectedDigest                          string `json:"expectedDigest"`
	TTLSeconds                              int    `json:"ttlSeconds"`
	Reason                                  string `json:"reason"`
	FindingsReviewed                        bool   `json:"findingsReviewed"`
}
type Approval struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenantId"`
	BundleID      string     `json:"bundleId"`
	BundleDigest  string     `json:"bundleDigest"`
	PlanHash      string     `json:"planHash"`
	PolicyHash    string     `json:"policyHash"`
	Subject       string     `json:"subject"`
	Reason        string     `json:"reason"`
	Mode          string     `json:"mode"`
	State         string     `json:"state"`
	Version       uint64     `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	ConsumedBy    string     `json:"consumedBy,omitempty"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	RevokedBy     string     `json:"revokedBy,omitempty"`
	RevokedReason string     `json:"revokedReason,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
}
type RevokeInput struct {
	TenantID, BundleID, ApprovalID, Subject string
	IdempotencyKey                          string `json:"idempotencyKey"`
	ExpectedVersion                         uint64 `json:"expectedVersion"`
	Reason                                  string `json:"reason"`
}
type StartInput struct {
	TenantID, BundleID, Subject string
	IdempotencyKey              string `json:"idempotencyKey"`
	ExpectedDigest              string `json:"expectedDigest"`
	ApprovalID                  string `json:"approvalId"`
	Scenario                    string `json:"scenario"`
}
type Simulation struct {
	ID          string      `json:"id"`
	TenantID    string      `json:"tenantId"`
	BundleID    string      `json:"bundleId"`
	ApprovalID  string      `json:"approvalId"`
	Subject     string      `json:"subject"`
	Scenario    string      `json:"scenario"`
	State       string      `json:"state"`
	ErrorCode   string      `json:"errorCode,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
	CompletedAt *time.Time  `json:"completedAt,omitempty"`
	WorkflowID  string      `json:"workflowId"`
	Operations  []Operation `json:"operations"`
}
type Operation struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	RunID       string     `json:"runId"`
	Ordinal     int        `json:"ordinal"`
	ActionID    string     `json:"actionId"`
	ActionHash  string     `json:"actionHash"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	Fence       int64      `json:"fence"`
	Owner       string     `json:"-"`
	LeaseUntil  *time.Time `json:"leaseUntil,omitempty"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	ReceiptID   string     `json:"receiptId,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Ref is the only workflow/activity input. Business values and credentials stay
// in application stores and never enter Temporal history.
type Ref struct {
	TenantID    string `json:"tenantId"`
	RunID       string `json:"runId"`
	OperationID string `json:"operationId,omitempty"`
}
type OutboxMessage struct {
	ID, TenantID, RunID, WorkflowID, Owner string
	Fence                                  int64
	Attempts                               int
}
type Policy interface {
	// Authorize also verifies current catalog bindings and returns a policy stamp.
	Authorize(context.Context, string, Bundle, string) (string, error)
	Revalidate(context.Context, Bundle) error
}
type SimulationService struct {
	Store        *Store
	Policy       Policy
	Owner        string
	LeaseSeconds int
}

func NewSimulationService(store *Store, policy Policy, owner string, leaseSeconds int) *SimulationService {
	return &SimulationService{Store: store, Policy: policy, Owner: owner, LeaseSeconds: leaseSeconds}
}
func validKey(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value
}
func eligible(b Bundle) bool {
	if !b.Valid() || b.Plan.State != "READY_FOR_REVIEW" || len(b.Plan.Issues) != 0 {
		return false
	}
	for _, s := range b.Plan.Services {
		if s.Review.State != "SUCCEEDED" || (s.ConfigCoverage != "CANDIDATES_FOUND" && s.ConfigCoverage != "NOT_INVOLVED") || (s.ExperimentCoverage != "CANDIDATES_FOUND" && s.ExperimentCoverage != "NOT_INVOLVED") {
			return false
		}
	}
	seen := map[string]bool{}
	for _, action := range b.Plan.Actions {
		a := action
		a.Hash = ""
		if action.ID == "" || seen[action.ID] || planning.Hash(a) != action.Hash {
			return false
		}
		seen[action.ID] = true
		switch action.Operation {
		case "config.create", "config.update", "config.no_change", "experiment.create_draft", "experiment.update_draft":
		default:
			return false
		}
	}
	return true
}
