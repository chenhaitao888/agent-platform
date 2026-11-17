package delivery

import (
	"context"
	"errors"
	"strings"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

func (s *WriteService) bindings(ctx context.Context, b Bundle) ([]WriteBinding, error) {
	if !eligible(b) || len(b.Plan.Actions) == 0 {
		return nil, ErrUnready
	}
	result := []WriteBinding{}
	seen, baselines := map[string]bool{}, map[string]string{}
	for _, a := range b.Plan.Actions {
		writer, err := s.Policy.Writer(ctx, a.Kind, a.Target)
		if err != nil || writer == nil {
			return nil, ErrPolicy
		}
		probe := planning.Clone(b.Plan)
		probe.Actions = []planning.Action{a}
		probe.Hash = ""
		probe.Hash = planning.Hash(probe)
		if _, err := resourcewrite.Prepare(probe, 0, "validation", writer.Describe(), time.Now().UTC().Add(time.Hour), nil); err != nil {
			return nil, ErrUnready
		}
		resource := planning.Hash(struct {
			Kind   string
			Target planning.Target
		}{a.Kind, a.Target})
		baseline := planning.Hash(struct {
			Version string
			Reader  planning.Descriptor
		}{a.ResourceVersion, a.Descriptor})
		if seen[resource+":"+a.Key] || baselines[resource] != "" && baselines[resource] != baseline {
			return nil, ErrUnready
		}
		seen[resource+":"+a.Key] = true
		baselines[resource] = baseline
		result = append(result, WriteBinding{ActionID: a.ID, ActionHash: a.Hash, Kind: a.Kind, Target: a.Target, Writer: writer.Describe()})
	}
	return result, nil
}
func (s *WriteService) approvalStamp(ctx context.Context, subject string, b Bundle) (string, error) {
	if !s.AllowSelfApproval && (subject == b.CreatedBy || subject == b.Plan.CreatedBy) {
		return "", ErrPolicy
	}
	stamp, err := s.Policy.Authorize(ctx, subject, b, "approve_write")
	if err != nil || stamp == "" {
		return "", ErrPolicy
	}
	return planning.Hash(struct {
		Stamp        string
		SelfApproval bool
	}{stamp, s.AllowSelfApproval}), nil
}
func (s *WriteService) Approve(ctx context.Context, in ApprovalInput) (WriteApproval, bool, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || in.TTLSeconds < 1 || in.TTLSeconds > 3600 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 || !in.FindingsReviewed {
		return WriteApproval{}, false, ErrInvalid
	}
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return WriteApproval{}, false, err
	}
	if b.Digest != in.ExpectedDigest {
		return WriteApproval{}, false, ErrConflict
	}
	stamp, err := s.approvalStamp(ctx, in.Subject, b)
	if err != nil {
		return WriteApproval{}, false, err
	}
	bindings, err := s.bindings(ctx, b)
	if err != nil {
		return WriteApproval{}, false, err
	}
	in.PolicyHash = stamp
	return s.Store.approveWrite(ctx, in, bindings)
}
func (s *WriteService) Start(ctx context.Context, in WriteStartInput) (WriteExecution, bool, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || in.ApprovalID == "" {
		return WriteExecution{}, false, ErrInvalid
	}
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return WriteExecution{}, false, err
	}
	if b.Digest != in.ExpectedDigest {
		return WriteExecution{}, false, ErrConflict
	}
	stamp, err := s.Policy.Authorize(ctx, in.Subject, b, "execute_write")
	if err != nil || stamp == "" {
		return WriteExecution{}, false, ErrPolicy
	}
	// Starting only consumes the grant and enqueues work. The worker rechecks
	// both principals, bindings, code and resources before any external attempt.
	return s.Store.startWrite(ctx, in, stamp)
}
func (s *WriteService) Revoke(ctx context.Context, in RevokeInput) (WriteApproval, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || in.ExpectedVersion == 0 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 {
		return WriteApproval{}, ErrInvalid
	}
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return WriteApproval{}, err
	}
	if _, err := s.Policy.Authorize(ctx, in.Subject, b, "revoke_write"); err != nil {
		return WriteApproval{}, ErrPolicy
	}
	return s.Store.revokeWrite(ctx, in)
}
func (s *WriteService) Begin(ctx context.Context, ref Ref) ([]string, error) {
	r, err := s.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, o := range r.Operations {
		ids = append(ids, o.ID)
	}
	return ids, nil
}
func (s *WriteService) check(ctx context.Context, b Bundle, a WriteApproval, r WriteExecution, prefix []resourcewrite.Completed) error {
	stamp, err := s.approvalStamp(ctx, a.Subject, b)
	if err != nil || stamp != a.PolicyHash {
		return ErrPolicy
	}
	stamp, err = s.Policy.Authorize(ctx, r.Subject, b, "execute_write")
	if err != nil || stamp == "" || stamp != r.PolicyHash {
		return ErrPolicy
	}
	bindings, err := s.bindings(ctx, b)
	if err != nil || planning.Hash(bindings) != planning.Hash(a.Bindings) || a.BundleDigest != b.Digest || a.PlanHash != b.Plan.Hash {
		return ErrPolicy
	}
	return s.Policy.Revalidate(ctx, b, prefix)
}

// Step never resends a persisted intent, including after a crash before the
// socket write. Only the transaction which first stores it can call Apply.
func (s *WriteService) Step(ctx context.Context, ref Ref) (bool, error) {
	r, err := s.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		return false, err
	}
	n, err := writeOrdinal(r, ref.OperationID)
	if err != nil {
		return false, err
	}
	o := r.Operations[n]
	if o.State == "VERIFIED" {
		return true, nil
	}
	if o.State == "REJECTED" || o.State == "DRIFTED" || o.State == "BLOCKED" {
		return false, nil
	}
	if o.Intent != nil {
		return s.reconcile(ctx, ref, o)
	}
	if r.State != "QUEUED" && r.State != "RUNNING" {
		return false, nil
	}
	b, err := s.Store.Get(ctx, ref.TenantID, r.BundleID)
	if err != nil {
		return false, err
	}
	a, err := s.Store.GetWriteApproval(ctx, ref.TenantID, r.BundleID, r.ApprovalID)
	if err != nil {
		return false, err
	}
	// Stop inactive grants before external revalidation; an unavailable reader
	// must not keep an expired/revoked run retrying indefinitely. The dispatch
	// transaction below checks again using the authoritative database clock.
	if !activeWriteApproval(a, r.ID, time.Now().UTC()) {
		return false, s.blockUnsent(ctx, ref, "approval_inactive")
	}
	prefix, err := writePrefix(r, n)
	if err != nil {
		return false, err
	}
	if err := s.check(ctx, b, a, r, prefix); err != nil {
		if errors.Is(err, ErrPolicy) || errors.Is(err, ErrDrift) {
			return false, s.blockUnsent(ctx, ref, "authorization_or_input_changed")
		}
		if errors.Is(err, resourcewrite.ErrInvalid) || errors.Is(err, ErrCorrupt) {
			return false, ErrCorrupt
		}
		return false, ErrUnavailable
	}
	if n >= len(a.Bindings) {
		return false, ErrCorrupt
	}
	intent, err := resourcewrite.Prepare(b.Plan, n, o.ID, a.Bindings[n].Writer, a.ExpiresAt, prefix)
	if err != nil {
		return false, ErrCorrupt
	}
	writer, err := s.Policy.Writer(ctx, intent.Kind, intent.Target)
	if err != nil || writer == nil || writer.Describe() != intent.Writer {
		return false, s.blockUnsent(ctx, ref, "writer_changed")
	}
	dispatch := false
	blocked := false
	err = s.Store.changeWrite(ctx, ref, func(current *WriteExecution, grant WriteApproval, now time.Time) error {
		operation := &current.Operations[n]
		if operation.Intent != nil {
			return nil
		}
		if operation.State != "NOT_STARTED" || current.State != "QUEUED" && current.State != "RUNNING" {
			return nil
		}
		if !activeWriteApproval(grant, current.ID, now) {
			haltWrite(current, "approval_inactive")
			blocked = true
			return nil
		}
		currentPrefix, err := writePrefix(*current, n)
		if err != nil {
			return err
		}
		if planning.Hash(currentPrefix) != planning.Hash(prefix) {
			return ErrBusy
		}
		// Commit marker before sending. The waiting window suppresses duplicate
		// activities while the first attempt is in flight; expiry permits GET only.
		lookup := now.Add(30 * time.Second)
		operation.Intent = &intent
		operation.State = "DISPATCHED"
		operation.DispatchedAt = &now
		operation.LookupAfter = &lookup
		current.State = "RUNNING"
		dispatch = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !dispatch {
		if blocked {
			return false, nil
		}
		return false, ErrBusy
	}
	receipt, err := writer.Apply(ctx, intent)
	if err != nil || !receipt.ValidFor(intent) {
		return false, s.unknown(ctx, ref, "outcome_unknown")
	}
	if err := s.recordReceipt(ctx, ref, receipt); err != nil {
		return false, err
	}
	return s.verify(ctx, ref, writer, intent, receipt)
}

func (s *WriteService) reconcile(ctx context.Context, ref Ref, o WriteOperation) (bool, error) {
	if o.Receipt == nil && o.State == "DISPATCHED" && o.LookupAfter != nil && time.Now().Before(*o.LookupAfter) {
		return false, ErrBusy
	}
	writer, err := s.Policy.Writer(ctx, o.Intent.Kind, o.Intent.Target)
	if err != nil || writer == nil || writer.Describe() != o.Intent.Writer {
		return false, s.unknown(ctx, ref, "reconciliation_unavailable")
	}
	receipt := o.Receipt
	if receipt == nil {
		found, err := writer.Lookup(ctx, *o.Intent)
		if err != nil || !found.ValidFor(*o.Intent) {
			return false, s.unknown(ctx, ref, "outcome_unknown")
		}
		if err := s.recordReceipt(ctx, ref, found); err != nil {
			return false, err
		}
		receipt = &found
	}
	return s.verify(ctx, ref, writer, *o.Intent, *receipt)
}
func (s *WriteService) verify(ctx context.Context, ref Ref, writer resourcewrite.Writer, intent resourcewrite.Intent, receipt resourcewrite.Receipt) (bool, error) {
	if receipt.Status == "REJECTED" {
		return false, nil
	}
	verified, err := writer.Verify(ctx, intent, receipt)
	if err != nil {
		return false, s.unknown(ctx, ref, "verification_unavailable")
	}
	valid := verified.ValidFor(intent, receipt)
	drift := verified.State == "DRIFTED" && verified.Code == "resource_changed" && verified.OperationID == intent.OperationID && verified.IntentHash == intent.Hash && verified.ReceiptID == receipt.ID && !verified.ObservedAt.IsZero() && !verified.ObservedAt.Before(receipt.RecordedAt)
	if !valid && !drift {
		return false, s.unknown(ctx, ref, "verification_unavailable")
	}
	err = s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		o := &r.Operations[n]
		if o.Intent == nil || o.Intent.Hash != intent.Hash || o.Receipt == nil || planning.Hash(*o.Receipt) != planning.Hash(receipt) {
			return ErrCorrupt
		}
		if o.Verification != nil {
			return nil
		}
		o.Verification = &verified
		o.LookupAfter = nil
		o.ErrorCode = ""
		if valid {
			o.State = "VERIFIED"
		} else {
			o.State = "DRIFTED"
			o.ErrorCode = "resource_changed"
			haltWrite(r, "resource_changed")
		}
		return nil
	})
	return valid, err
}
func (s *WriteService) recordReceipt(ctx context.Context, ref Ref, receipt resourcewrite.Receipt) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		o := &r.Operations[n]
		if o.Intent == nil || !receipt.ValidFor(*o.Intent) {
			return ErrCorrupt
		}
		if o.Receipt != nil {
			if planning.Hash(*o.Receipt) != planning.Hash(receipt) {
				return ErrCorrupt
			}
			return nil
		}
		o.Receipt = &receipt
		o.LookupAfter = nil
		o.ErrorCode = ""
		if receipt.Status == "REJECTED" {
			o.State = "REJECTED"
			o.ErrorCode = receipt.Code
			haltWrite(r, receipt.Code)
		} else {
			o.State = "APPLIED"
		}
		return nil
	})
}
func haltWrite(r *WriteExecution, code string) {
	r.State = "BLOCKED"
	r.ErrorCode = code
	for n := range r.Operations {
		if r.Operations[n].State == "NOT_STARTED" {
			r.Operations[n].State = "BLOCKED"
			r.Operations[n].ErrorCode = code
		}
	}
}
func (s *WriteService) blockUnsent(ctx context.Context, ref Ref, code string) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		// Another worker may already have advanced the resource while this
		// worker was checking the old baseline. Re-read its persisted outcome.
		if r.Operations[n].Intent != nil {
			return ErrBusy
		}
		haltWrite(r, code)
		return nil
	})
}
func (s *WriteService) unknown(ctx context.Context, ref Ref, code string) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		o := &r.Operations[n]
		if o.Verification != nil || o.State == "REJECTED" {
			return nil
		}
		o.LookupAfter = nil
		o.ErrorCode = code
		if o.Receipt == nil {
			o.State = "UNKNOWN"
		}
		haltWrite(r, code)
		r.State = "UNKNOWN"
		return nil
	})
}
func (s *WriteService) Block(ctx context.Context, ref Ref, code string) error {
	// Only fixed internal codes may be persisted; never copy connector errors.
	if code != "authorization_or_input_changed" && code != "writer_changed" && code != "invalid_saved_input" {
		return ErrInvalid
	}
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error { haltWrite(r, code); return nil })
}
func (s *WriteService) Finish(ctx context.Context, ref Ref) (string, error) {
	state := ""
	err := s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, now time.Time) error {
		counts := map[string]int{}
		for _, o := range r.Operations {
			counts[o.State]++
		}
		switch {
		case counts["UNKNOWN"]+counts["DISPATCHED"]+counts["APPLIED"] > 0:
			code := r.ErrorCode
			if code == "" {
				code = "outcome_unknown"
			}
			haltWrite(r, code)
			r.State = "UNKNOWN"
		case counts["DRIFTED"] > 0:
			r.State = "DRIFTED"
		case counts["REJECTED"] > 0:
			r.State = "FAILED"
		case counts["VERIFIED"] == len(r.Operations) && len(r.Operations) > 0:
			r.State = "SUCCEEDED"
			r.ErrorCode = ""
		case counts["BLOCKED"] > 0:
			r.State = "BLOCKED"
		default:
			return ErrBusy
		}
		state = r.State
		if state != "UNKNOWN" && r.CompletedAt == nil {
			r.CompletedAt = &now
		}
		return nil
	})
	return state, err
}

// Reconcile is read-only against the provider, even if permission/approval has
// since expired. It never unblocks remaining work or creates a new intent.
func (s *WriteService) Reconcile(ctx context.Context, ref Ref) (string, error) {
	r, err := s.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		return "", err
	}
	for _, o := range r.Operations {
		if o.Intent == nil || o.Verification != nil || o.State == "REJECTED" {
			continue
		}
		step := ref
		step.OperationID = o.ID
		if _, err := s.reconcile(ctx, step, o); err != nil {
			return "", err
		}
	}
	return s.Finish(ctx, ref)
}
