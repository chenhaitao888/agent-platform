package httpapi

import (
	"agent-platform/backend/internal/delivery"
	"context"
	"errors"
	"net/http"
	"time"
)

func (h *handler) simulationBundle(w http.ResponseWriter, r *http.Request) (delivery.Bundle, bool) {
	b, ok := h.loadDelivery(w, r)
	if !ok {
		return b, false
	}
	if h.simulation == nil {
		writeError(w, 503, "simulation_unavailable", "simulation is not configured")
		return b, false
	}
	if _, err := h.simulation.Policy.Authorize(r.Context(), changeActor(r).Subject, b, "read"); err != nil {
		writeSimulationError(w, err)
		return b, false
	}
	return b, true
}
func (h *handler) approveSimulation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.ApprovalInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	actor := changeActor(r)
	in.ApprovalInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.Subject = actor.Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a, created, err := h.simulation.Approve(ctx, in.ApprovalInput)
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, a)
}
func (h *handler) revokeSimulation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.RevokeInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	in.RevokeInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.ApprovalID = r.PathValue("approvalId")
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a, err := h.simulation.Revoke(ctx, in.RevokeInput)
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	writeJSON(w, 200, a)
}
func (h *handler) listSimulationApprovals(w http.ResponseWriter, r *http.Request) {
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, err := h.archives.ListApprovals(ctx, b.TenantID, b.ID)
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []delivery.Approval `json:"items"`
	}{items})
}
func (h *handler) startSimulation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.StartInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	in.StartInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	run, created, err := h.simulation.Start(ctx, in.StartInput)
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	status := 200
	if created {
		status = 202
	}
	writeJSON(w, status, run)
}
func (h *handler) listSimulations(w http.ResponseWriter, r *http.Request) {
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, err := h.archives.ListSimulations(ctx, b.TenantID, b.ID)
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []delivery.Simulation `json:"items"`
	}{items})
}
func (h *handler) getSimulation(w http.ResponseWriter, r *http.Request) {
	b, ok := h.simulationBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	run, err := h.archives.GetSimulation(ctx, delivery.Ref{TenantID: b.TenantID, RunID: r.PathValue("simulationId")})
	if err != nil {
		writeSimulationError(w, err)
		return
	}
	if run.BundleID != b.ID {
		writeSimulationError(w, delivery.ErrNotFound)
		return
	}
	writeJSON(w, 200, run)
}
func writeSimulationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, delivery.ErrPolicy):
		writeError(w, 403, "simulation_forbidden", "simulation permission or binding is not authorized")
	case errors.Is(err, delivery.ErrInvalid):
		writeError(w, 400, "validation_error", "invalid simulation input")
	case errors.Is(err, delivery.ErrUnready):
		writeError(w, 409, "simulation_unready", "review the ready plan and Findings before simulation approval")
	case errors.Is(err, delivery.ErrApproval):
		writeError(w, 409, "approval_inactive", "approval is expired, revoked or already used")
	case errors.Is(err, delivery.ErrVersion):
		writeError(w, 409, "version_conflict", "approval version changed")
	default:
		writeArchiveError(w, err)
	}
}
