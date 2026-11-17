package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"agent-platform/backend/internal/delivery"
)

func (h *handler) writeBundle(w http.ResponseWriter, r *http.Request) (delivery.Bundle, bool) {
	b, ok := h.loadDelivery(w, r)
	if !ok {
		return b, false
	}
	if h.writes == nil {
		writeError(w, 503, "writes_unavailable", "resource writes are not configured")
		return b, false
	}
	return b, true
}
func (h *handler) writeAccess(w http.ResponseWriter, r *http.Request) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	allowed := func(op string) bool {
		_, err := h.writes.Policy.Authorize(r.Context(), changeActor(r).Subject, b, op)
		return err == nil
	}
	writeJSON(w, 200, struct {
		TenantID   string `json:"tenantId"`
		BundleID   string `json:"bundleId"`
		CanApprove bool   `json:"canApprove"`
		CanExecute bool   `json:"canExecute"`
		CanRevoke  bool   `json:"canRevoke"`
	}{b.TenantID, b.ID, allowed("approve_write"), allowed("execute_write"), allowed("revoke_write")})
}
func (h *handler) approveWrite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.ApprovalInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	in.ApprovalInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a, created, err := h.writes.Approve(ctx, in.ApprovalInput)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, a)
}
func (h *handler) revokeWrite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.RevokeInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	in.RevokeInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.ApprovalID = r.PathValue("approvalId")
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a, err := h.writes.Revoke(ctx, in.RevokeInput)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	writeJSON(w, 200, a)
}
func (h *handler) listWriteApprovals(w http.ResponseWriter, r *http.Request) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, err := h.archives.ListWriteApprovals(ctx, b.TenantID, b.ID)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []delivery.WriteApproval `json:"items"`
	}{items})
}
func (h *handler) startWrite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.WriteStartInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	in.WriteStartInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	run, created, err := h.writes.Start(ctx, in.WriteStartInput)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	status := 200
	if created {
		status = 202
	}
	writeJSON(w, status, run.Public())
}
func (h *handler) listWriteExecutions(w http.ResponseWriter, r *http.Request) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, err := h.archives.ListWriteExecutions(ctx, b.TenantID, b.ID)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	for n := range items {
		items[n] = items[n].Public()
	}
	writeJSON(w, 200, struct {
		Items []delivery.WriteExecution `json:"items"`
	}{items})
}
func (h *handler) loadWriteExecution(w http.ResponseWriter, r *http.Request) (delivery.WriteExecution, bool) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return delivery.WriteExecution{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	run, err := h.archives.GetWriteExecution(ctx, delivery.Ref{TenantID: b.TenantID, RunID: r.PathValue("executionId")})
	if err == nil && run.BundleID != b.ID {
		err = delivery.ErrNotFound
	}
	if err != nil {
		writeExecutionError(w, err)
		return delivery.WriteExecution{}, false
	}
	return run, true
}
func (h *handler) getWriteExecution(w http.ResponseWriter, r *http.Request) {
	if run, ok := h.loadWriteExecution(w, r); ok {
		writeJSON(w, 200, run.Public())
	}
}
func (h *handler) reconcileWrite(w http.ResponseWriter, r *http.Request) {
	// Reconciliation changes the local ledger only; no caller-supplied operation,
	// intent, retry key or external request can be injected through this endpoint.
	var in struct {
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	run, ok := h.loadWriteExecution(w, r)
	if !ok {
		return
	}
	if run.State != "UNKNOWN" {
		writeError(w, 409, "write_not_unknown", "only an unknown execution needs reconciliation")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ref := delivery.Ref{TenantID: run.TenantID, RunID: run.ID}
	if _, err := h.writes.Reconcile(ctx, ref); err != nil {
		writeExecutionError(w, err)
		return
	}
	run, err := h.archives.GetWriteExecution(ctx, ref)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	writeJSON(w, 200, run.Public())
}
func writeExecutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, delivery.ErrPolicy):
		writeError(w, 403, "write_forbidden", "current write permission or binding is not authorized")
	case errors.Is(err, delivery.ErrInvalid):
		writeError(w, 400, "validation_error", "invalid write request")
	case errors.Is(err, delivery.ErrUnready):
		writeError(w, 409, "write_unready", "a ready plan and reviewed Findings are required")
	case errors.Is(err, delivery.ErrApproval):
		writeError(w, 409, "approval_inactive", "write approval is expired, revoked or consumed")
	case errors.Is(err, delivery.ErrVersion):
		writeError(w, 409, "version_conflict", "approval version changed")
	case errors.Is(err, delivery.ErrBusy):
		writeError(w, 409, "write_busy", "write outcome is still in flight")
	default:
		writeArchiveError(w, err)
	}
}
