package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/task"
)

func (h changeHandler) review(w http.ResponseWriter, r *http.Request) {
	var input struct {
		change.ReviewInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &input) || !optionalChangeTenant(w, r, input.TenantID) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	result, replayed, err := h.service.Review(ctx, changeActor(r), r.PathValue("id"), input.ReviewInput)
	if err != nil {
		writeChangeError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
func (h changeHandler) reviews(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.ListReviews(changeActor(r), r.PathValue("id"))
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Items []change.ReviewRun `json:"items"`
	}{values})
}
func (h changeHandler) getReview(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetReview(changeActor(r), r.PathValue("id"), r.PathValue("reviewId"))
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (h *handler) canReadLinkedTask(r *http.Request, child task.Task) bool {
	if child.Change.ChangeID == "" {
		return true
	}
	_, ok := h.catalog.Authorized(changeActor(r), child.Change.ServiceID, child.Change.Environment, "read")
	return ok
}

// Apply after route matching so PathValue is available, and before any legacy
// handler. Linked children are read through existing endpoints; mutations go
// through the change service so snapshot-scoped retry and authorization agree.
func (h *handler) guardLinkedTask(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := changeActor(r).TenantID
		id := r.PathValue("id")
		if strings.HasPrefix(r.URL.Path, "/api/v1/artifacts/") {
			metadata, ok := h.artifacts.Get(id, tenant)
			if ok {
				id = metadata.TaskID
			} else {
				id = ""
			}
		}
		child, ok := h.tasks.Get(id, tenant)
		if ok && child.Change.ChangeID != "" {
			if !h.canReadLinkedTask(r, child) {
				writeChangeError(w, change.ErrForbidden)
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, 409, "change_review_required", "start or retry this review from its change snapshot")
				return
			}
		}
		next(w, r)
	}
}
