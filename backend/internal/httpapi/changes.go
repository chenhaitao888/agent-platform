package httpapi

import (
	"agent-platform/backend/internal/strictjson"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
)

// WithChanges preserves all existing single-repository endpoints. Both routers
// require the same authenticated context; the deployment wraps them together.
func WithChanges(existing http.Handler, service *change.Service) http.Handler {
	mux := http.NewServeMux()
	h := changeHandler{service: service}
	mux.HandleFunc("GET /api/v1/services", h.services)
	mux.HandleFunc("GET /api/v1/changes", h.list)
	mux.HandleFunc("POST /api/v1/changes", h.create)
	mux.HandleFunc("GET /api/v1/changes/{id}", h.get)
	mux.HandleFunc("POST /api/v1/changes/{id}/snapshots", h.freeze)
	mux.HandleFunc("POST /api/v1/changes/{id}/reviews", h.review)
	mux.HandleFunc("GET /api/v1/changes/{id}/reviews", h.reviews)
	mux.HandleFunc("GET /api/v1/changes/{id}/reviews/{reviewId}", h.getReview)
	mux.Handle("/", existing)
	return requireIdentity(mux)
}

type changeHandler struct{ service *change.Service }

func changeActor(r *http.Request) servicecatalog.Actor {
	identity := r.Context().Value(identityKey{}).(authenticatedIdentity)
	return servicecatalog.Actor{Subject: identity.principal.Subject, TenantID: identity.principal.TenantID, RepositoryIDs: identity.repositories}
}
func optionalChangeTenant(w http.ResponseWriter, r *http.Request, tenant *string) bool {
	if tenant == nil {
		return true
	}
	_, ok := checkedTenant(w, r, *tenant)
	return ok
}
func (h changeHandler) services(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, struct {
		Items []servicecatalog.PublicEntry `json:"items"`
	}{h.service.Services(changeActor(r))})
}
func (h changeHandler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, struct {
		Items []change.ChangeRequest `json:"items"`
	}{h.service.List(changeActor(r))})
}
func (h changeHandler) get(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Get(changeActor(r), r.PathValue("id"))
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (h changeHandler) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		change.CreateInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &input) || !optionalChangeTenant(w, r, input.TenantID) {
		return
	}
	value, replayed, err := h.service.Create(changeActor(r), input.CreateInput)
	if err != nil {
		writeChangeError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, value)
}
func (h changeHandler) freeze(w http.ResponseWriter, r *http.Request) {
	var input struct {
		change.SnapshotInput
		TenantID *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &input) || !optionalChangeTenant(w, r, input.TenantID) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	value, replayed, err := h.service.Freeze(ctx, changeActor(r), r.PathValue("id"), input.SnapshotInput)
	if err != nil {
		writeChangeError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, value)
}
func writeChangeError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadRequest, "validation_error", "invalid change input"
	switch {
	case errors.Is(err, change.ErrForbidden):
		status, code, message = 403, "service_forbidden", change.ErrForbidden.Error()
	case errors.Is(err, change.ErrNotFound):
		status, code, message = 404, "not_found", change.ErrNotFound.Error()
	case errors.Is(err, change.ErrVersionConflict):
		status, code, message = 409, "version_conflict", change.ErrVersionConflict.Error()
	case errors.Is(err, change.ErrIdempotencyConflict):
		status, code, message = 409, "idempotency_conflict", change.ErrIdempotencyConflict.Error()
	case errors.Is(err, change.ErrInProgress):
		status, code, message = 409, "snapshot_in_progress", change.ErrInProgress.Error()
	case errors.Is(err, change.ErrSnapshotNotReady):
		status, code, message = 409, "snapshot_not_ready", change.ErrSnapshotNotReady.Error()
	case errors.Is(err, change.ErrReviewComplete):
		status, code, message = 409, "review_complete", change.ErrReviewComplete.Error()
	case errors.Is(err, review.ErrReviewUnavailable):
		status, code, message = 503, "review_unavailable", review.ErrReviewUnavailable.Error()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 408, "request_canceled", "request canceled; read the change before retrying"
	}
	writeError(w, status, code, message)
}

func decodeChangeRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer func() { _ = r.Body.Close() }()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeDecodeError(w, err)
		return false
	}
	if err := strictjson.Decode(data, destination); err != nil {
		writeDecodeError(w, err)
		return false
	}
	return true
}
