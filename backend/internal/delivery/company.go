package delivery

import (
	"context"
	"slices"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

// CompanyOperation is part of the existing locked/digested execution ledger.
// A separate dispatch marker covers the second side effect (ticket submission).
// Historical executions omit this field and keep delivery.write.v1 semantics.
type CompanyOperation struct {
	State        string                            `json:"state"`
	RequestID    string                            `json:"requestId"`
	DispatchedAt *time.Time                        `json:"dispatchedAt,omitempty"`
	LookupAfter  *time.Time                        `json:"lookupAfter,omitempty"`
	Ticket       *resourcewrite.CompanyTicket      `json:"ticket,omitempty"`
	Observation  *resourcewrite.CompanyObservation `json:"observation,omitempty"`
	ErrorCode    string                            `json:"errorCode,omitempty"`
}

func companyDone(state string) bool {
	return slices.Contains([]string{"PUBLISHED", "APPROVAL_REJECTED", "APPROVAL_WITHDRAWN", "APPROVAL_EXPIRED", "PUBLICATION_FAILED", "PUBLICATION_DRIFTED", "BLOCKED"}, state)
}
func companyState(r WriteExecution, fallback string) string {
	pending, failed, unknown := false, false, false
	for _, op := range r.Operations {
		if op.Company == nil {
			continue
		}
		if op.State != "VERIFIED" {
			continue
		}
		state := op.Company.State
		pending = pending || !companyDone(state)
		failed = failed || companyDone(state) && state != "PUBLISHED"
		unknown = unknown || state == "SUBMISSION_UNKNOWN"
	}
	if unknown {
		return "UNKNOWN"
	}
	if pending {
		return "WAITING_COMPANY"
	}
	if failed {
		return "FAILED"
	}
	return fallback
}

func (s *WriteService) companyWriter(ctx context.Context, r WriteExecution, o WriteOperation) (resourcewrite.CompanyWriter, error) {
	if o.Intent == nil || o.Receipt == nil || o.Verification == nil || !o.Verification.ValidFor(*o.Intent, *o.Receipt) {
		return nil, ErrUnready
	}
	b, err := s.Store.Get(ctx, r.TenantID, r.BundleID)
	if err != nil {
		return nil, err
	}
	if _, err := s.Policy.Authorize(ctx, r.Subject, b, "read"); err != nil {
		return nil, ErrPolicy
	}
	w, err := s.Policy.Writer(ctx, o.Intent.Kind, o.Intent.Target)
	if err != nil || w == nil || w.Describe() != o.Intent.Writer {
		return nil, ErrPolicy
	}
	company, ok := w.(resourcewrite.CompanyWriter)
	if !ok {
		return nil, ErrPolicy
	}
	return company, nil
}

// CompanySubmit may send exactly once, only while the original grant and both
// principals remain valid. A saved marker always takes the read-only path.
func (s *WriteService) CompanySubmit(ctx context.Context, ref Ref) (bool, error) {
	r, err := s.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		return false, err
	}
	n, err := writeOrdinal(r, ref.OperationID)
	if err != nil {
		return false, err
	}
	o := r.Operations[n]
	if o.Company == nil {
		return true, nil
	}
	if o.State != "VERIFIED" {
		return false, nil
	}
	if o.Intent == nil || o.Receipt == nil || o.Verification == nil || !o.Verification.ValidFor(*o.Intent, *o.Receipt) {
		return false, ErrCorrupt
	}
	c := o.Company
	if c.Ticket != nil || o.Intent.Operation == "config.no_change" {
		return true, nil
	}
	if companyDone(c.State) {
		return false, nil
	}
	w, err := s.companyWriter(ctx, r, o)
	if err != nil {
		return false, err
	}
	if o.Receipt.Company != nil && o.Receipt.Company.Ticket != nil {
		t := *o.Receipt.Company.Ticket
		if !t.Matches(*o.Intent, *o.Receipt, o.Intent.OperationID) {
			return false, ErrCorrupt
		}
		return true, s.recordCompanyTicket(ctx, ref, t, true)
	}
	if c.DispatchedAt != nil {
		if c.LookupAfter != nil && time.Now().Before(*c.LookupAfter) {
			return false, ErrBusy
		}
		t, err := w.LookupTicket(ctx, *o.Intent, *o.Receipt, c.RequestID)
		if err != nil || !t.Matches(*o.Intent, *o.Receipt, c.RequestID) {
			return false, s.companyUnknown(ctx, ref)
		}
		return true, s.recordCompanyTicket(ctx, ref, t, false)
	}
	b, err := s.Store.Get(ctx, r.TenantID, r.BundleID)
	if err != nil {
		return false, err
	}
	a, err := s.Store.GetWriteApproval(ctx, r.TenantID, r.BundleID, r.ApprovalID)
	if err != nil {
		return false, err
	}
	prefix, err := writePrefix(r, len(r.Operations))
	if err != nil {
		return false, s.blockCompany(ctx, ref, "save_incomplete")
	}
	if !activeWriteApproval(a, r.ID, time.Now().UTC()) {
		return false, s.blockCompany(ctx, ref, "approval_inactive")
	}
	if err := s.check(ctx, b, a, r, prefix); err != nil {
		return false, s.blockCompany(ctx, ref, "authorization_or_input_changed")
	}
	dispatch := false
	err = s.Store.changeWrite(ctx, ref, func(current *WriteExecution, grant WriteApproval, now time.Time) error {
		op := &current.Operations[n]
		if op.Company.DispatchedAt != nil || op.Company.State != "NOT_SUBMITTED" {
			return nil
		}
		if !activeWriteApproval(grant, current.ID, now) {
			stopCompany(current, "approval_inactive")
			return nil
		}
		if planning.Hash(current.Operations) != planning.Hash(r.Operations) {
			return ErrBusy
		}
		until := now.Add(30 * time.Second)
		op.Company.DispatchedAt = &now
		op.Company.LookupAfter = &until
		op.Company.State = "SUBMITTING"
		dispatch = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !dispatch {
		return false, nil
	}
	t, err := w.Submit(ctx, *o.Intent, *o.Receipt, c.RequestID)
	if err != nil || !t.Matches(*o.Intent, *o.Receipt, c.RequestID) {
		return false, s.companyUnknown(ctx, ref)
	}
	return true, s.recordCompanyTicket(ctx, ref, t, false)
}

func stopCompany(r *WriteExecution, code string) {
	for n := range r.Operations {
		op := r.Operations[n]
		c := op.Company
		// Atomic save+submit has already created a ticket. Stopping later
		// submissions must not hide its approval/publication tracking.
		if op.Intent != nil && op.Intent.Operation == "config.no_change" || op.Receipt != nil && op.Receipt.Company != nil && op.Receipt.Company.Ticket != nil {
			continue
		}
		if c != nil && c.State == "NOT_SUBMITTED" {
			c.State = "BLOCKED"
			c.ErrorCode = code
		}
	}
}
func (s *WriteService) blockCompany(ctx context.Context, ref Ref, code string) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error { stopCompany(r, code); return nil })
}
func (s *WriteService) companyUnknown(ctx context.Context, ref Ref) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		c := r.Operations[n].Company
		if c.Ticket == nil {
			c.State = "SUBMISSION_UNKNOWN"
			c.ErrorCode = "ticket_outcome_unknown"
			c.LookupAfter = nil
			stopCompany(r, "ticket_outcome_unknown")
		}
		return nil
	})
}
func (s *WriteService) recordCompanyTicket(ctx context.Context, ref Ref, t resourcewrite.CompanyTicket, combined bool) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		o := &r.Operations[n]
		c := o.Company
		requestID := c.RequestID
		if combined {
			requestID = o.Intent.OperationID
		}
		if !t.Matches(*o.Intent, *o.Receipt, requestID) {
			return ErrCorrupt
		}
		if c.Ticket != nil {
			if c.Ticket.ID != t.ID {
				return ErrCorrupt
			}
			return nil
		}
		c.Ticket = &t
		c.State = "WAITING_APPROVAL"
		c.LookupAfter = nil
		c.ErrorCode = ""
		return nil
	})
}

// CompanyObserve never submits a new work order. Missing permissions/bindings
// keep the durable workflow waiting; expiry of the WRITE grant is irrelevant.
func (s *WriteService) CompanyObserve(ctx context.Context, ref Ref) (bool, error) {
	r, err := s.Store.GetWriteExecution(ctx, ref)
	if err != nil {
		return false, err
	}
	n, err := writeOrdinal(r, ref.OperationID)
	if err != nil {
		return false, err
	}
	o := r.Operations[n]
	if o.Company == nil || companyDone(o.Company.State) {
		return true, nil
	}
	b, readErr := s.Store.Get(ctx, r.TenantID, r.BundleID)
	if readErr != nil {
		return false, readErr
	}
	if _, readErr = s.Policy.Authorize(ctx, r.Subject, b, "read"); readErr != nil {
		return false, s.companyTrackingError(ctx, ref, "tracking_permission_or_binding_changed")
	}
	if o.Intent != nil && slices.Contains([]string{"DISPATCHED", "UNKNOWN", "APPLIED"}, o.State) {
		ok, err := s.reconcile(ctx, ref, o)
		if err != nil || !ok {
			return false, err
		}
		r, err = s.Store.GetWriteExecution(ctx, ref)
		if err != nil {
			return false, err
		}
		o = r.Operations[n]
	}
	if o.State != "VERIFIED" {
		return true, nil
	}
	if o.Intent == nil || o.Receipt == nil || o.Verification == nil || !o.Verification.ValidFor(*o.Intent, *o.Receipt) {
		return false, ErrCorrupt
	}
	if o.Company.Ticket == nil && o.Intent.Operation != "config.no_change" {
		// Recover only already-dispatched requests or the ticket embedded in an
		// atomic save+submit receipt. Never call CompanySubmit for a new request.
		if o.Company.DispatchedAt == nil && (o.Receipt.Company == nil || o.Receipt.Company.Ticket == nil) {
			return true, s.blockCompany(ctx, ref, "submission_not_started")
		}
		ok, err := s.CompanySubmit(ctx, ref)
		if err != nil {
			return false, s.companyTrackingError(ctx, ref, "tracking_unavailable")
		}
		if !ok {
			return false, nil
		}
		r, err = s.Store.GetWriteExecution(ctx, ref)
		if err != nil {
			return false, err
		}
		o = r.Operations[n]
	}
	w, err := s.companyWriter(ctx, r, o)
	if err != nil {
		return false, s.companyTrackingError(ctx, ref, "tracking_permission_or_binding_changed")
	}
	observation, err := w.Observe(ctx, *o.Intent, *o.Receipt, o.Company.Ticket)
	if err != nil {
		return false, s.companyTrackingError(ctx, ref, "tracking_unavailable")
	}
	if !validCompanyObservation(o, observation) {
		return false, s.companyTrackingError(ctx, ref, "tracking_evidence_invalid")
	}
	err = s.Store.changeWrite(ctx, ref, func(current *WriteExecution, _ WriteApproval, _ time.Time) error {
		c := current.Operations[n].Company
		if companyDone(c.State) {
			return nil
		}
		if c.Observation != nil && observation.ObservedAt.Before(c.Observation.ObservedAt) {
			return nil
		}
		c.Observation = &observation
		c.State = observation.State
		c.ErrorCode = ""
		return nil
	})
	return companyDone(observation.State), err
}
func validCompanyObservation(o WriteOperation, v resourcewrite.CompanyObservation) bool {
	if v.ObservedAt.IsZero() || v.ObservedAt.Before(o.Receipt.RecordedAt) {
		return false
	}
	if o.Intent.Operation != "config.no_change" {
		if v.Ticket == nil || o.Company.Ticket == nil || v.Ticket.ID != o.Company.Ticket.ID || !v.Ticket.Matches(*o.Intent, *o.Receipt, o.Company.Ticket.RequestID) {
			return false
		}
		switch v.State {
		case "WAITING_APPROVAL":
			return v.Ticket.State == "PENDING"
		case "APPROVAL_REJECTED", "APPROVAL_WITHDRAWN", "APPROVAL_EXPIRED":
			return v.State == "APPROVAL_"+v.Ticket.State
		}
		p := v.Publication
		if v.Ticket.State != "APPROVED" || p == nil || p.TicketID != v.Ticket.ID || p.ChangeID != v.Ticket.ChangeID || p.ChangeVersion != v.Ticket.ChangeVersion || p.Target != o.Intent.Target {
			return false
		}
		switch v.State {
		case "WAITING_PUBLICATION":
			return p.State == "PENDING"
		case "PUBLISHING":
			return p.State == "PUBLISHING"
		case "PUBLICATION_FAILED":
			return p.State == "FAILED"
		}
		if p.State != "PUBLISHED" || !planning.ValidIdentifier(p.ID) || !planning.ValidIdentifier(p.EffectiveVersion) {
			return false
		}
		if v.State == "PUBLISHED" && v.EffectiveVersion != p.EffectiveVersion {
			return false
		}
	}
	if !planning.ValidIdentifier(v.EffectiveVersion) || len(v.ValueHash) != 64 {
		return false
	}
	return v.State == "PUBLISHED" && v.ValueHash == o.Intent.ValueDigest() || v.State == "PUBLICATION_DRIFTED" && (v.ValueHash != o.Intent.ValueDigest() || v.Publication != nil && v.EffectiveVersion != v.Publication.EffectiveVersion)
}
func (s *WriteService) companyTrackingError(ctx context.Context, ref Ref, code string) error {
	return s.Store.changeWrite(ctx, ref, func(r *WriteExecution, _ WriteApproval, _ time.Time) error {
		n, err := writeOrdinal(*r, ref.OperationID)
		if err != nil {
			return err
		}
		if !companyDone(r.Operations[n].Company.State) {
			r.Operations[n].Company.ErrorCode = code
		}
		return nil
	})
}
