package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
)

func (h *handler) archiveAuthorized(actor servicecatalog.Actor, b delivery.Bundle, operation string) bool {
	if actor.TenantID != b.TenantID {
		return false
	}
	for _, s := range b.Snapshot.Services {
		if _, ok := h.catalog.Authorized(actor, s.ServiceID, b.Environment, operation); !ok || s.Repository == nil || !slices.Contains(actor.RepositoryIDs, s.Repository.RepositoryID) {
			return false
		}
	}
	return len(b.Snapshot.Services) > 0
}
func (h *handler) archivePlan(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IdempotencyKey   string  `json:"idempotencyKey"`
		ExpectedPlanHash string  `json:"expectedPlanHash"`
		TenantID         *string `json:"tenantId,omitempty"`
	}
	if !decodeChangeRequest(w, r, &input) || !optionalChangeTenant(w, r, input.TenantID) {
		return
	}
	actor := changeActor(r)
	request := delivery.Request{TenantID: actor.TenantID, ChangeID: r.PathValue("id"), PlanID: r.PathValue("planId"), PlanHash: input.ExpectedPlanHash, IdempotencyKey: input.IdempotencyKey}
	if !request.Valid() {
		writeChangeError(w, change.ErrValidation)
		return
	}
	if h.archives == nil {
		writeArchiveError(w, delivery.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	prior, found, err := h.archives.Replay(ctx, request)
	if found && prior.ID != "" && !h.archiveAuthorized(actor, prior, "archive") {
		writeChangeError(w, change.ErrForbidden)
		return
	}
	if err != nil {
		writeArchiveError(w, err)
		return
	}
	if found {
		writeJSON(w, 200, prior.Public())
		return
	}
	bundle, err := h.captureDelivery(actor, request)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) || errors.Is(err, change.ErrForbidden) {
			writeChangeError(w, err)
		} else {
			writeArchiveError(w, err)
		}
		return
	}
	stored, created, err := h.archives.Save(ctx, request, bundle)
	if err != nil {
		writeArchiveError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/deliveries/"+stored.ID)
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, stored.Public())
}
func (h *handler) captureDelivery(actor servicecatalog.Actor, r delivery.Request) (delivery.Bundle, error) {
	if h.changes == nil {
		return delivery.Bundle{}, delivery.ErrUnavailable
	}
	current, err := h.changes.Get(actor, r.ChangeID)
	if err != nil {
		return delivery.Bundle{}, err
	}
	plan, err := h.changes.GetPlan(actor, r.ChangeID, r.PlanID)
	if err != nil {
		return delivery.Bundle{}, err
	}
	if plan.Hash != r.PlanHash || plan.State == "RUNNING" {
		return delivery.Bundle{}, delivery.ErrConflict
	}
	b := delivery.Bundle{TenantID: actor.TenantID, ChangeID: current.ID, Title: current.Title, Description: current.Description, Environment: current.Environment, CreatedBy: actor.Subject, Plan: plan, Evidence: []delivery.Evidence{}}
	for _, snapshot := range current.Snapshots {
		if snapshot.ID == plan.SnapshotID {
			b.Snapshot = snapshot
			break
		}
	}
	if !h.archiveAuthorized(actor, b, "archive") {
		return delivery.Bundle{}, change.ErrForbidden
	}
	for _, source := range plan.Services {
		if source.Review.ExecutionID == "" {
			continue
		}
		t, ok := h.tasks.Get(source.Review.TaskID, actor.TenantID)
		if !ok || t.Change.ChangeID != current.ID || t.Change.SnapshotID != plan.SnapshotID || t.Change.ServiceID != source.ServiceID {
			return delivery.Bundle{}, delivery.ErrInvalid
		}
		execution, ok := h.reviews.GetExecutionByID(review.GetInput{TaskID: t.ID, TenantID: actor.TenantID}, source.Review.ExecutionID)
		if !ok {
			return delivery.Bundle{}, delivery.ErrInvalid
		}
		evidence := delivery.Evidence{ServiceID: source.ServiceID, Execution: execution}
		if execution.Artifact != nil {
			metadata, content, ok := h.artifacts.GetContent(execution.Artifact.ID, actor.TenantID)
			if !ok || metadata != *execution.Artifact {
				return delivery.Bundle{}, delivery.ErrInvalid
			}
			evidence.Content = content
		}
		b.Evidence = append(b.Evidence, evidence)
	}
	return b, nil
}
func (h *handler) deliveryContext(w http.ResponseWriter, r *http.Request) (servicecatalog.Actor, bool) {
	actor := changeActor(r)
	if r.URL.Query().Has("tenantId") {
		if _, ok := queryTenant(w, r); !ok {
			return actor, false
		}
	}
	if h.archives == nil {
		writeArchiveError(w, delivery.ErrUnavailable)
		return actor, false
	}
	return actor, true
}
func (h *handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.deliveryContext(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	values, err := h.archives.List(ctx, actor.TenantID)
	if err != nil {
		writeArchiveError(w, err)
		return
	}
	items := []delivery.Bundle{}
	for _, value := range values {
		if h.archiveAuthorized(actor, value, "read") {
			items = append(items, value.Public())
		}
	}
	writeJSON(w, 200, struct {
		Items []delivery.Bundle `json:"items"`
	}{items})
}
func (h *handler) loadDelivery(w http.ResponseWriter, r *http.Request) (delivery.Bundle, bool) {
	actor, ok := h.deliveryContext(w, r)
	if !ok {
		return delivery.Bundle{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	value, err := h.archives.Get(ctx, actor.TenantID, r.PathValue("deliveryId"))
	if err != nil {
		writeArchiveError(w, err)
		return delivery.Bundle{}, false
	}
	if !h.archiveAuthorized(actor, value, "read") {
		writeChangeError(w, change.ErrForbidden)
		return delivery.Bundle{}, false
	}
	return value, true
}
func (h *handler) getDelivery(w http.ResponseWriter, r *http.Request) {
	if value, ok := h.loadDelivery(w, r); ok {
		writeJSON(w, 200, value.Public())
	}
}
func (h *handler) getDeliveryArtifact(w http.ResponseWriter, r *http.Request) {
	value, ok := h.loadDelivery(w, r)
	if !ok {
		return
	}
	for _, e := range value.Evidence {
		if e.Execution.Artifact != nil && e.Execution.Artifact.ID == r.PathValue("artifactId") {
			w.Header().Set("Content-Type", e.Execution.Artifact.MediaType)
			w.Header().Set("Content-Length", strconv.Itoa(len(e.Content)))
			w.Header().Set("ETag", `"`+e.Execution.Artifact.SHA256+`"`)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(200)
			_, _ = w.Write(e.Content)
			return
		}
	}
	writeArchiveError(w, delivery.ErrNotFound)
}
func writeArchiveError(w http.ResponseWriter, err error) {
	status, code, message := 503, "archive_unavailable", "delivery storage is not configured or unavailable"
	switch {
	case errors.Is(err, delivery.ErrNotFound):
		status, code, message = 404, "not_found", delivery.ErrNotFound.Error()
	case errors.Is(err, delivery.ErrConflict):
		status, code, message = 409, "archive_conflict", delivery.ErrConflict.Error()
	case errors.Is(err, delivery.ErrInvalid):
		status, code, message = 409, "archive_source_invalid", delivery.ErrInvalid.Error()
	case errors.Is(err, delivery.ErrCorrupt):
		code, message = "archive_integrity_failed", delivery.ErrCorrupt.Error()
	}
	writeError(w, status, code, message)
}
