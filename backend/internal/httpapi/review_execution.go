package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

type executeReviewRequest struct {
	RequestID                string `json:"requestId"`
	TenantID                 string `json:"tenantId"`
	IdempotencyKey           string `json:"idempotencyKey"`
	ExpectedWorkspaceVersion uint64 `json:"expectedWorkspaceVersion"`
}

func (h *handler) getReviewExecution(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" || exceedsIdentifierLimit(tenantID) {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required and must fit the supported length")
		return
	}
	current, ok := h.reviews.GetExecution(review.GetInput{TaskID: r.PathValue("id"), TenantID: tenantID})
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "review execution not found")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (h *handler) executeReview(w http.ResponseWriter, r *http.Request) {
	var request executeReviewRequest
	if !decodeStrictRequest(w, r, &request) {
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.RequestID == "" || request.TenantID == "" || request.IdempotencyKey == "" || request.ExpectedWorkspaceVersion == 0 || exceedsIdentifierLimit(request.RequestID, request.TenantID, request.IdempotencyKey) {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, tenantId, idempotencyKey and expectedWorkspaceVersion are required and must fit supported limits")
		return
	}
	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.reviews.Execute(r.Context(), review.ExecuteInput{RequestID: request.RequestID, TenantID: request.TenantID, TaskID: r.PathValue("id"), IdempotencyKey: request.IdempotencyKey, ExpectedWorkspaceVersion: request.ExpectedWorkspaceVersion})
	if err != nil {
		h.writeReviewError(w, r, request, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tasks/"+r.PathValue("id")+"/review?tenantId="+url.QueryEscape(request.TenantID))
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Execution)
}

func (h *handler) writeReviewError(w http.ResponseWriter, r *http.Request, request executeReviewRequest, err error) {
	status, code, message := http.StatusBadGateway, "review_execution_failed", "review execution failed"
	switch {
	case errors.Is(err, task.ErrTaskNotFound), errors.Is(err, review.ErrWorkspaceNotFound):
		status, code, message = http.StatusNotFound, "not_found", "task or workspace not found"
	case errors.Is(err, review.ErrInvalidTaskType):
		status, code, message = http.StatusConflict, "invalid_task_type", "review execution requires a PR_REVIEW task"
	case errors.Is(err, workspace.ErrTaskNotQueued):
		status, code, message = http.StatusConflict, "task_not_queued", "task must be QUEUED before review execution"
	case errors.Is(err, review.ErrWorkspaceNotReady):
		status, code, message = http.StatusConflict, "workspace_not_ready", "workspace must be READY before review execution"
	case errors.Is(err, workspace.ErrVersionConflict):
		status, code, message = http.StatusConflict, "version_conflict", "workspace version does not match expectedWorkspaceVersion"
	case errors.Is(err, review.ErrExecutionIdempotencyConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", "idempotency key already used with different review input"
	case errors.Is(err, review.ErrExecutionInProgress):
		status, code, message = http.StatusConflict, "review_in_progress", "review execution is already in progress"
	case errors.Is(err, review.ErrReviewUnavailable):
		status, code, message = http.StatusServiceUnavailable, "review_unavailable", "review execution is not configured"
	case errors.Is(err, repository.ErrDiffUnavailable):
		status, code, message = http.StatusServiceUnavailable, "diff_unavailable", "repository diff is unavailable"
	case errors.Is(err, repository.ErrDiffTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "diff_too_large", "repository diff exceeds the supported size"
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusRequestTimeout, "review_canceled", "review execution was canceled"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "review_timeout", "review execution exceeded its time limit"
	case errors.Is(err, review.ErrInvalidRunInput):
		code, message = "review_input_invalid", "platform review input is invalid"
	case errors.Is(err, review.ErrInvalidFindings):
		code, message = "review_findings_invalid", "review findings failed validation"
	case errors.Is(err, review.ErrOutputUnavailable):
		code, message = "review_output_unavailable", "review output is unavailable"
	case errors.Is(err, review.ErrOutputTooLarge):
		code, message = "review_output_too_large", "review output exceeds the supported size"
	}
	if status >= 500 {
		h.logServerError(r, request.RequestID, request.TenantID, code, errors.New(message))
	}
	writeError(w, status, code, message)
}
