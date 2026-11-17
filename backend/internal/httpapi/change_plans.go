package httpapi

import (
	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/planning"
	"context"
	"net/http"
)

func (h changeHandler) plan(w http.ResponseWriter, r *http.Request) {
	var input struct {
		change.PlanInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &input) || !optionalChangeTenant(w, r, input.TenantID) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.service.PlanningTimeout())
	defer cancel()
	result, replayed, err := h.service.Plan(ctx, changeActor(r), r.PathValue("id"), input.PlanInput)
	if err != nil {
		writeChangeError(w, err)
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	writeJSON(w, status, result)
}
func (h changeHandler) plans(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.ListPlans(changeActor(r), r.PathValue("id"))
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []planning.Plan `json:"items"`
	}{values})
}
func (h changeHandler) getPlan(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetPlan(changeActor(r), r.PathValue("id"), r.PathValue("planId"))
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
