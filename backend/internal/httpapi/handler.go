package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/backend/internal/task"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type handler struct {
	tasks *task.Store
}

type createTaskRequest struct {
	RequestID      string `json:"requestId"`
	IdempotencyKey string `json:"idempotencyKey"`
	TenantID       string `json:"tenantId"`
	Type           string `json:"type"`
	Goal           string `json:"goal"`
}

type updateTaskRequest struct {
	RequestID       string      `json:"requestId"`
	IdempotencyKey  string      `json:"idempotencyKey"`
	TenantID        string      `json:"tenantId"`
	ExpectedVersion uint64      `json:"expectedVersion"`
	Status          task.Status `json:"status"`
}

type listTasksResponse struct {
	Items []task.Task `json:"items"`
}

type listTaskEventsResponse struct {
	Items []task.Event `json:"items"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func NewHandler() http.Handler {
	h := &handler{
		tasks: task.NewStore(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("POST /api/v1/tasks", h.createTask)
	mux.HandleFunc("GET /api/v1/tasks", h.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.getTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", h.listTaskEvents)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", h.updateTask)

	return mux
}

func (h *handler) updateTask(w http.ResponseWriter, r *http.Request) {
	var request updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.Status = task.Status(strings.TrimSpace(string(request.Status)))
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}
	if request.ExpectedVersion == 0 || request.Status != task.StatusQueued {
		writeError(w, http.StatusBadRequest, "validation_error", "expectedVersion and status QUEUED are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	updated, err := h.tasks.Transition(task.TransitionInput{
		TaskID:          r.PathValue("id"),
		RequestID:       request.RequestID,
		TenantID:        request.TenantID,
		IdempotencyKey:  request.IdempotencyKey,
		ExpectedVersion: request.ExpectedVersion,
		Status:          request.Status,
	})
	if errors.Is(err, task.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if errors.Is(err, task.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different task update")
		return
	}
	if errors.Is(err, task.ErrVersionConflict) {
		writeError(w, http.StatusConflict, "version_conflict", "task version does not match expectedVersion")
		return
	}
	if errors.Is(err, task.ErrInvalidTransition) {
		writeError(w, http.StatusConflict, "invalid_transition", "task status transition is not allowed")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "task update failed")
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

func (h *handler) listTaskEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	events, ok := h.tasks.ListEvents(r.PathValue("id"), tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}

	writeJSON(w, http.StatusOK, listTaskEventsResponse{Items: events})
}

func (h *handler) listTasks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, listTasksResponse{Items: h.tasks.List()})
}

func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	found, ok := h.tasks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}

	writeJSON(w, http.StatusOK, found)
}

func (h *handler) createTask(w http.ResponseWriter, r *http.Request) {
	var request createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.Type = strings.TrimSpace(request.Type)
	request.Goal = strings.TrimSpace(request.Goal)
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}
	if request.Type == "" || request.Goal == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "type and goal are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.tasks.Create(task.CreateInput{
		RequestID:      request.RequestID,
		TenantID:       request.TenantID,
		IdempotencyKey: request.IdempotencyKey,
		Type:           request.Type,
		Goal:           request.Goal,
	})
	if errors.Is(err, task.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different task content")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "task creation failed")
		return
	}
	w.Header().Set("Location", "/api/v1/tasks/"+result.Task.ID)
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Task)
}

func healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: "agent-platform-api",
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error:   code,
		Message: message,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
