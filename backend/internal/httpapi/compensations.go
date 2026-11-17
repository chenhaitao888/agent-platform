package httpapi

import (
	"context"
	"net/http"
	"time"

	"agent-platform/backend/internal/delivery"
)

func (h *handler) prepareCompensation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		delivery.CompensationInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &in) || !optionalChangeTenant(w, r, in.TenantID) {
		return
	}
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	in.CompensationInput.TenantID = b.TenantID
	in.BundleID = b.ID
	in.ExecutionID = r.PathValue("executionId")
	in.Subject = changeActor(r).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, created, err := h.writes.PrepareCompensation(ctx, in.CompensationInput)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, p)
}
func (h *handler) listCompensations(w http.ResponseWriter, r *http.Request) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, err := h.archives.ListCompensations(ctx, b.TenantID, b.ID)
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []delivery.CompensationPlan `json:"items"`
	}{items})
}
func (h *handler) getCompensation(w http.ResponseWriter, r *http.Request) {
	b, ok := h.writeBundle(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := h.archives.GetCompensation(ctx, b.TenantID, b.ID, r.PathValue("compensationId"))
	if err != nil {
		writeExecutionError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
