package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type handler struct {
	tasks      *task.Store
	workspaces *workspace.Manager
}

type createTaskRequest struct {
	RequestID      string                   `json:"requestId"`
	IdempotencyKey string                   `json:"idempotencyKey"`
	TenantID       string                   `json:"tenantId"`
	Type           string                   `json:"type"`
	Goal           string                   `json:"goal"`
	Repository     task.RepositoryReference `json:"repository"`
}

type updateTaskRequest struct {
	RequestID       string      `json:"requestId"`
	IdempotencyKey  string      `json:"idempotencyKey"`
	TenantID        string      `json:"tenantId"`
	ExpectedVersion uint64      `json:"expectedVersion"`
	Status          task.Status `json:"status"`
}

type createWorkspaceRequest struct {
	RequestID      string `json:"requestId"`
	IdempotencyKey string `json:"idempotencyKey"`
	TenantID       string `json:"tenantId"`
}

type prepareWorkspaceRequest struct {
	RequestID       string `json:"requestId"`
	IdempotencyKey  string `json:"idempotencyKey"`
	TenantID        string `json:"tenantId"`
	ExpectedVersion uint64 `json:"expectedVersion"`
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
	return NewHandlerWithRepositoryVerifier(repository.UnavailableVerifier{})
}

func NewHandlerWithRepositoryVerifier(verifier repository.ReferenceVerifier) http.Handler {
	tasks := task.NewStore()
	return newHandler(tasks, workspace.NewManager(tasks, verifier))
}

func NewHandlerWithWorkspacePreparer(verifier repository.ReferenceVerifier, preparer workspace.Preparer, root string) (http.Handler, error) {
	tasks := task.NewStore()
	workspaces, err := workspace.NewManagerWithPreparer(tasks, verifier, preparer, root)
	if err != nil {
		return nil, err
	}
	return newHandler(tasks, workspaces), nil
}

func newHandler(tasks *task.Store, workspaces *workspace.Manager) http.Handler {
	h := &handler{tasks: tasks, workspaces: workspaces}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("POST /api/v1/tasks", h.createTask)
	mux.HandleFunc("GET /api/v1/tasks", h.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.getTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", h.listTaskEvents)
	mux.HandleFunc("POST /api/v1/tasks/{id}/workspace", h.createWorkspace)
	mux.HandleFunc("POST /api/v1/tasks/{id}/workspace/prepare", h.prepareWorkspace)
	mux.HandleFunc("GET /api/v1/tasks/{id}/workspace", h.getWorkspace)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", h.updateTask)

	return mux
}

func (h *handler) prepareWorkspace(w http.ResponseWriter, r *http.Request) {
	var request prepareWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" || request.ExpectedVersion == 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey, tenantId and expectedVersion are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.workspaces.Prepare(r.Context(), workspace.PrepareInput{
		TenantID:        request.TenantID,
		TaskID:          r.PathValue("id"),
		IdempotencyKey:  request.IdempotencyKey,
		ExpectedVersion: request.ExpectedVersion,
	})
	if errors.Is(err, workspace.ErrWorkspaceNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if errors.Is(err, workspace.ErrVersionConflict) {
		writeError(w, http.StatusConflict, "version_conflict", "workspace version does not match expectedVersion")
		return
	}
	if errors.Is(err, workspace.ErrInvalidState) {
		writeError(w, http.StatusConflict, "invalid_workspace_state", "workspace must be REGISTERED before preparation")
		return
	}
	if errors.Is(err, workspace.ErrPreparationIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different workspace preparation")
		return
	}
	if errors.Is(err, workspace.ErrPreparationUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "workspace_preparation_unavailable", "workspace preparation is unavailable")
		return
	}
	if errors.Is(err, workspace.ErrPreparationFailed) {
		writeError(w, http.StatusServiceUnavailable, "workspace_preparation_failed", "workspace preparation failed")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "workspace preparation failed")
		return
	}

	writeJSON(w, http.StatusOK, result.Workspace)
}

func (h *handler) getWorkspace(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	found, ok := h.workspaces.Get(r.PathValue("id"), tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (h *handler) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var request createWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.workspaces.Register(r.Context(), workspace.RegisterInput{
		TenantID:       request.TenantID,
		TaskID:         r.PathValue("id"),
		IdempotencyKey: request.IdempotencyKey,
	})
	if errors.Is(err, workspace.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if errors.Is(err, workspace.ErrTaskNotQueued) {
		writeError(w, http.StatusConflict, "task_not_queued", "task must be QUEUED before workspace registration")
		return
	}
	if errors.Is(err, workspace.ErrWorkspaceExists) {
		writeError(w, http.StatusConflict, "workspace_already_exists", "workspace already exists for task")
		return
	}
	if errors.Is(err, workspace.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used for another workspace registration")
		return
	}
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "repository_not_found", "repository was not found or is not accessible")
		return
	}
	if errors.Is(err, repository.ErrBaseCommitNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "base_commit_not_found", "base commit was not found in repository")
		return
	}
	if errors.Is(err, repository.ErrHeadCommitNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "head_commit_not_found", "head commit was not found in repository")
		return
	}
	if errors.Is(err, repository.ErrVerificationUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "repository_verification_unavailable", "repository verification is unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "workspace registration failed")
		return
	}

	w.Header().Set("Location", "/api/v1/tasks/"+result.Workspace.TaskID+"/workspace")
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Workspace)
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
	request.Repository.Provider = strings.TrimSpace(request.Repository.Provider)
	request.Repository.RepositoryID = strings.TrimSpace(request.Repository.RepositoryID)
	request.Repository.BaseSHA = strings.TrimSpace(request.Repository.BaseSHA)
	request.Repository.HeadSHA = strings.TrimSpace(request.Repository.HeadSHA)
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}
	if request.Type == "" || request.Goal == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "type and goal are required")
		return
	}
	if request.Repository.Provider == "" || request.Repository.RepositoryID == "" || request.Repository.BaseSHA == "" || request.Repository.HeadSHA == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "repository provider, repositoryId, baseSha and headSha are required")
		return
	}
	if request.Repository.Provider != "gitlab" {
		writeError(w, http.StatusBadRequest, "validation_error", "repository provider must be gitlab")
		return
	}
	if !isGitObjectID(request.Repository.BaseSHA) || !isGitObjectID(request.Repository.HeadSHA) {
		writeError(w, http.StatusBadRequest, "validation_error", "baseSha and headSha must be 40 or 64 hexadecimal characters")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.tasks.Create(task.CreateInput{
		RequestID:      request.RequestID,
		TenantID:       request.TenantID,
		IdempotencyKey: request.IdempotencyKey,
		Type:           request.Type,
		Goal:           request.Goal,
		Repository:     request.Repository,
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

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		isUpperHex := character >= 'A' && character <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
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
