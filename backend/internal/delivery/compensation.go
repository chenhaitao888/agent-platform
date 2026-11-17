package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

type CompensationOrigin struct {
	ReportID      string `json:"reportId"`
	BundleID      string `json:"bundleId"`
	BundleDigest  string `json:"bundleDigest"`
	ExecutionID   string `json:"executionId"`
	ExecutionHash string `json:"executionHash"`
	ManualItems   int    `json:"manualItems"`
}
type CompensationInput struct {
	TenantID, BundleID, ExecutionID, Subject string
	IdempotencyKey                           string `json:"idempotencyKey"`
	ExpectedExecutionHash                    string `json:"expectedExecutionHash"`
	Reason                                   string `json:"reason"`
}
type CompensationItem struct {
	OperationID     string          `json:"operationId"`
	ActionID        string          `json:"actionId"`
	Kind            string          `json:"kind"`
	Key             string          `json:"key"`
	Target          planning.Target `json:"target"`
	Disposition     string          `json:"disposition"`
	Code            string          `json:"code"`
	ReceiptID       string          `json:"receiptId,omitempty"`
	RestoreActionID string          `json:"restoreActionId,omitempty"`
	CurrentValue    *planning.Value `json:"currentValue,omitempty"`
	RestoreValue    *planning.Value `json:"restoreValue,omitempty"`
}
type CompensationPlan struct {
	SchemaVersion       string             `json:"schemaVersion"`
	ID                  string             `json:"id"`
	TenantID            string             `json:"tenantId"`
	SourceBundleID      string             `json:"sourceBundleId"`
	SourceBundleDigest  string             `json:"sourceBundleDigest"`
	SourceExecutionID   string             `json:"sourceExecutionId"`
	SourceExecutionHash string             `json:"sourceExecutionHash"`
	CreatedBy           string             `json:"createdBy"`
	CreatedAt           time.Time          `json:"createdAt"`
	Reason              string             `json:"reason"`
	State               string             `json:"state"`
	ErrorCode           string             `json:"errorCode,omitempty"`
	Items               []CompensationItem `json:"items"`
	Plan                planning.Plan      `json:"plan"`
	DerivedBundleID     string             `json:"derivedBundleId,omitempty"`
	DerivedBundleDigest string             `json:"derivedBundleDigest,omitempty"`
	Digest              string             `json:"digest"`
}
type CompensationReader interface {
	ReadCompensationConfig(context.Context, planning.Target) (planning.Descriptor, planning.ConfigState, error)
}

func WritePermission(b Bundle, operation string) string {
	if b.Compensation != nil {
		if operation == "approve_write" {
			return "approve_compensation"
		}
		if operation == "execute_write" {
			return "execute_compensation"
		}
	}
	return operation
}

func (s *WriteService) PrepareCompensation(ctx context.Context, in CompensationInput) (CompensationPlan, bool, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || len(in.ExpectedExecutionHash) != 64 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 {
		return CompensationPlan{}, false, ErrInvalid
	}
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return CompensationPlan{}, false, err
	}
	if b.Compensation != nil {
		return CompensationPlan{}, false, ErrUnready
	}
	if _, err := s.Policy.Authorize(ctx, in.Subject, b, "prepare_compensation"); err != nil {
		return CompensationPlan{}, false, ErrPolicy
	}
	// Replay before any provider read. A saved preparation remains a historical
	// observation even if the source or platform has subsequently changed.
	if prior, found, err := s.Store.replayCompensation(ctx, in); err != nil || found {
		return prior, false, err
	}
	r, err := s.Store.GetWriteExecution(ctx, Ref{TenantID: in.TenantID, RunID: in.ExecutionID})
	if err != nil {
		return CompensationPlan{}, false, err
	}
	if r.BundleID != b.ID {
		return CompensationPlan{}, false, ErrNotFound
	}
	if r.EvidenceDigest() != in.ExpectedExecutionHash {
		return CompensationPlan{}, false, ErrConflict
	}
	if !slices.Contains([]string{"SUCCEEDED", "FAILED", "BLOCKED", "DRIFTED", "UNKNOWN"}, r.State) {
		return CompensationPlan{}, false, ErrBusy
	}
	if err := validateCompensationSource(b, r); err != nil {
		return CompensationPlan{}, false, err
	}
	reader, ok := s.Policy.(CompensationReader)
	if !ok {
		return CompensationPlan{}, false, ErrConfiguration
	}
	report := CompensationPlan{SchemaVersion: "compensation-plan.v1", ID: "compensation-" + rand.Text(), TenantID: b.TenantID, SourceBundleID: b.ID, SourceBundleDigest: b.Digest, SourceExecutionID: r.ID, SourceExecutionHash: r.EvidenceDigest(), CreatedBy: in.Subject, CreatedAt: time.Now().UTC(), Reason: in.Reason, State: "MANUAL_ONLY", Items: []CompensationItem{}}
	report.Plan = planning.Plan{SchemaVersion: "change-plan.v1", ID: "compensation-plan-" + rand.Text(), Revision: 1, TenantID: b.TenantID, Environment: b.Environment, SnapshotID: b.Snapshot.ID, SnapshotHash: b.Snapshot.InputHash, CreatedBy: in.Subject, CreatedAt: report.CreatedAt, AnalyzerVersion: "compensation.v1", State: "READY_FOR_REVIEW", Actions: []planning.Action{}, Issues: []planning.Issue{}, Observations: []planning.Observation{}, Services: planning.Clone(b.Plan.Services)}
	for n := range report.Plan.Services {
		source := &report.Plan.Services[n]
		source.Analysis = nil
		source.Questions = nil
		source.Candidates = []planning.Candidate{}
		source.ConfigCoverage = "NOT_INVOLVED"
		source.ExperimentCoverage = "NOT_INVOLVED"
		source.ConfigReason = "no automated restoration for this service"
		source.ExperimentReason = "experiment disposition requires manual handling"
	}
	latest := map[string]string{}
	unresolved := r.State == "UNKNOWN" || r.CompletedAt == nil
	for _, o := range r.Operations {
		if o.Intent != nil && o.Receipt == nil || o.State == "APPLIED" || o.State == "DISPATCHED" || o.State == "UNKNOWN" {
			unresolved = true
		}
		if o.Receipt != nil && o.Receipt.Status == "APPLIED" {
			latest[compensationResource(o.Intent.Kind, o.Intent.Target)] = o.Receipt.ResourceVersion
		}
	}
	type observation struct {
		descriptor planning.Descriptor
		state      planning.ConfigState
		err        error
	}
	observed := map[string]observation{}
	for n := len(r.Operations) - 1; n >= 0; n-- {
		o, a := r.Operations[n], b.Plan.Actions[n]
		item := CompensationItem{OperationID: o.ID, ActionID: a.ID, Kind: a.Kind, Key: a.Key, Target: a.Target, Disposition: "MANUAL", Code: "unsupported_restore"}
		if o.Receipt != nil {
			item.ReceiptID = o.Receipt.ID
		}
		switch {
		case o.Intent == nil:
			item.Disposition = "NO_EFFECT"
			item.Code = "not_dispatched"
		case o.Receipt == nil:
			item.Disposition = "UNRESOLVED"
			item.Code = "outcome_unknown"
		case o.Receipt.Status == "REJECTED":
			item.Disposition = "NO_EFFECT"
			item.Code = "rejected"
		case a.Operation == "config.no_change":
			item.Disposition = "NO_EFFECT"
			item.Code = "no_change"
		case unresolved:
			item.Disposition = "UNRESOLVED"
			item.Code = "source_not_settled"
		case a.Operation == "config.create" || a.Operation == "experiment.create_draft":
			item.Code = "deletion_unsupported"
		case a.Kind == "experiment":
			item.Code = "experiment_restore_unsupported"
		case o.State != "VERIFIED":
			item.Code = "source_not_verified"
		default:
			key := compensationResource(a.Kind, a.Target)
			current, found := observed[key]
			if !found {
				current.descriptor, current.state, current.err = reader.ReadCompensationConfig(ctx, a.Target)
				observed[key] = current
			}
			item.Code = "resource_unavailable"
			if current.err == nil {
				item.Code = "resource_changed"
				if current.descriptor == a.Descriptor && planning.ValidConfigState(current.state, a.Target) && current.state.Version == latest[key] && current.state.SaveMode == o.Intent.Effect {
					for _, value := range current.state.Items {
						if value.Key != a.Key {
							continue
						}
						if value.Version != o.Receipt.ItemVersion || planning.Hash(value.Value) != o.Receipt.ValueHash {
							break
						}
						item.Code = "restore_constraints_changed"
						restore, err := planning.RestoreConfig(a, current.descriptor, current.state)
						if err == nil {
							restore.ID = "restore-" + o.ID
							restore.Hash = planning.Hash(restore)
							item.Disposition = "RESTORE_CONFIG"
							item.Code = "restore_previous_value"
							item.RestoreActionID = restore.ID
							item.CurrentValue = planning.Clone(restore.CurrentValue)
							item.RestoreValue = planning.Clone(a.CurrentValue)
							report.Plan.Actions = append(report.Plan.Actions, restore)
						}
						break
					}
				}
			}
		}
		report.Items = append(report.Items, item)
	}
	seen := map[string]bool{}
	for _, a := range report.Plan.Actions {
		key := compensationResource(a.Kind, a.Target)
		if !seen[key] {
			seen[key] = true
			report.Plan.Observations = append(report.Plan.Observations, planning.Observation{Kind: a.Kind, Target: a.Target, Descriptor: a.Descriptor, Version: a.ResourceVersion, ObservedAt: report.CreatedAt})
		}
		for n := range report.Plan.Services {
			for _, proposal := range a.Proposals {
				if proposal.ServiceID == report.Plan.Services[n].ServiceID {
					report.Plan.Services[n].ConfigCoverage = "CANDIDATES_FOUND"
					report.Plan.Services[n].ConfigReason = "receipt-bound configuration restoration"
				}
			}
		}
	}
	if unresolved {
		report.State = "BLOCKED"
		report.ErrorCode = "source_not_settled"
	} else if len(report.Plan.Actions) > 0 {
		report.State = "READY_FOR_REVIEW"
	} else {
		manual := false
		for _, item := range report.Items {
			manual = manual || item.Disposition == "MANUAL"
		}
		if !manual {
			report.State = "NO_ACTION_NEEDED"
		}
	}
	if report.State != "READY_FOR_REVIEW" {
		report.Plan.State = "BLOCKED"
		report.Plan.Issues = append(report.Plan.Issues, planning.Issue{Code: "compensation_not_executable"})
	}
	report.Plan.Hash = planning.Hash(report.Plan)
	derived := planning.Clone(b)
	derived.ID = "delivery-" + rand.Text()
	derived.CreatedBy = in.Subject
	derived.CreatedAt = report.CreatedAt
	derived.Title = "配置补偿 · " + b.ID
	derived.Description = in.Reason
	derived.Plan = report.Plan
	derived.Digest = ""
	manual := 0
	for _, item := range report.Items {
		if item.Disposition == "MANUAL" {
			manual++
		}
	}
	derived.Compensation = &CompensationOrigin{ReportID: report.ID, BundleID: b.ID, BundleDigest: b.Digest, ExecutionID: r.ID, ExecutionHash: r.EvidenceDigest(), ManualItems: manual}
	if report.State == "READY_FOR_REVIEW" {
		if err := s.Policy.Revalidate(ctx, derived, nil); err != nil {
			report.State = "BLOCKED"
			report.ErrorCode = "compensation_input_changed"
			if !errors.Is(err, ErrDrift) && !errors.Is(err, ErrPolicy) {
				report.ErrorCode = "compensation_revalidation_unavailable"
			}
			report.Plan.State = "BLOCKED"
			report.Plan.Issues = append(report.Plan.Issues, planning.Issue{Code: "compensation_input_changed"})
			report.Plan.Hash = ""
			report.Plan.Hash = planning.Hash(report.Plan)
		}
	}
	if ctx.Err() != nil {
		return CompensationPlan{}, false, ErrUnavailable
	}
	return s.Store.saveCompensation(ctx, in, report, derived)
}
func compensationResource(kind string, target planning.Target) string {
	return planning.Hash(struct {
		Kind   string
		Target planning.Target
	}{kind, target})
}

func validateCompensationSource(b Bundle, r WriteExecution) error {
	if !eligible(b) || r.Mode != "WRITE" || r.TenantID != b.TenantID || r.BundleID != b.ID || len(r.Operations) != len(b.Plan.Actions) {
		return ErrCorrupt
	}
	prefix := []resourcewrite.Completed{}
	seen := map[string]bool{}
	for n, o := range r.Operations {
		a := b.Plan.Actions[n]
		if !planning.ValidIdentifier(o.ID) || seen[o.ID] || o.ActionID != a.ID || o.ActionHash != a.Hash {
			return ErrCorrupt
		}
		seen[o.ID] = true
		if o.Intent == nil {
			if o.State != "BLOCKED" && o.State != "NOT_STARTED" || o.Receipt != nil || o.Verification != nil {
				return ErrCorrupt
			}
			continue
		}
		if !slices.Contains([]string{"DISPATCHED", "UNKNOWN", "APPLIED", "VERIFIED", "DRIFTED", "REJECTED"}, o.State) {
			return ErrCorrupt
		}
		i, err := resourcewrite.Prepare(b.Plan, n, o.ID, o.Intent.Writer, o.Intent.ExpiresAt, prefix)
		if err != nil || planning.Hash(i) != planning.Hash(*o.Intent) {
			return ErrCorrupt
		}
		if o.Receipt != nil && !o.Receipt.ValidFor(i) {
			return ErrCorrupt
		}
		if o.Receipt == nil && (o.State != "UNKNOWN" && o.State != "DISPATCHED" || o.Verification != nil) {
			return ErrCorrupt
		}
		if o.Receipt != nil && o.Receipt.Status == "REJECTED" && (o.State != "REJECTED" || o.Verification != nil) {
			return ErrCorrupt
		}
		if o.Receipt != nil && o.Receipt.Status == "APPLIED" && !slices.Contains([]string{"APPLIED", "VERIFIED", "DRIFTED"}, o.State) {
			return ErrCorrupt
		}
		if o.State == "DRIFTED" && (o.Verification == nil || o.Receipt == nil || o.Verification.State != "DRIFTED" || o.Verification.Code != "resource_changed" || o.Verification.OperationID != o.ID || o.Verification.IntentHash != i.Hash || o.Verification.ReceiptID != o.Receipt.ID || o.Verification.ObservedAt.Before(o.Receipt.RecordedAt)) {
			return ErrCorrupt
		}
		if o.State == "VERIFIED" {
			if o.Receipt == nil || o.Verification == nil || !o.Verification.ValidFor(i, *o.Receipt) {
				return ErrCorrupt
			}
			prefix = append(prefix, resourcewrite.Completed{Intent: i, Receipt: *o.Receipt, Verification: *o.Verification})
		} else if o.State == "REJECTED" && (o.Receipt == nil || o.Receipt.Status != "REJECTED") {
			return ErrCorrupt
		}
	}
	return nil
}
