package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type handler struct {
	tasks      *task.Store
	resolver   repository.ReferenceResolver
	workspaces *workspace.Manager
	reviews    *review.Service
	artifacts  *artifact.Store
	// 与 GitLab verifier 使用同一份 token；仅用于遮盖日志，不写入响应。
	logSecret string
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

type archiveDiffRequest struct {
	RequestID                string `json:"requestId"`
	IdempotencyKey           string `json:"idempotencyKey"`
	TenantID                 string `json:"tenantId"`
	ExpectedWorkspaceVersion uint64 `json:"expectedWorkspaceVersion"`
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

const (
	maxRequestBodyBytes = 64 << 10
	maxIdentifierBytes  = 256
	maxGoalBytes        = 4 << 10
)

func NewHandler() http.Handler {
	return NewHandlerWithRepositoryServices(repository.UnavailableVerifier{}, "")
}

func NewHandlerWithRepositoryServices(references repository.ReferenceServices, gitlabToken string) http.Handler {
	tasks := task.NewStore()
	return newHandler(tasks, workspace.NewManager(tasks, references), references, gitlabToken)
}

func NewHandlerWithWorkspacePreparer(verifier repository.ReferenceServices, preparer workspace.Preparer, root, gitlabToken string) (http.Handler, error) {
	return NewHandlerWithWorkspaceServices(verifier, preparer, repository.UnavailableDiffReader{}, root, gitlabToken)
}

func NewHandlerWithWorkspaceServices(
	verifier repository.ReferenceServices,
	preparer workspace.Preparer,
	diffReader repository.DiffReader,
	root string,
	gitlabToken string,
) (http.Handler, error) {
	tasks := task.NewStore()
	workspaces, err := workspace.NewManagerWithPreparer(tasks, verifier, preparer, root)
	if err != nil {
		return nil, err
	}
	return newHandlerWithDiffReader(tasks, workspaces, diffReader, verifier, gitlabToken), nil
}

func newHandler(tasks *task.Store, workspaces *workspace.Manager, resolver repository.ReferenceResolver, gitlabToken string) http.Handler {
	return newHandlerWithDiffReader(tasks, workspaces, repository.UnavailableDiffReader{}, resolver, gitlabToken)
}

func newHandlerWithDiffReader(tasks *task.Store, workspaces *workspace.Manager, diffReader repository.DiffReader, resolver repository.ReferenceResolver, gitlabToken string) http.Handler {
	artifacts := artifact.NewStore()
	h := &handler{
		tasks:      tasks,
		resolver:   resolver,
		workspaces: workspaces,
		reviews:    review.NewServiceWithArtifactStore(workspaces, diffReader, artifacts, tasks),
		artifacts:  artifacts,
		logSecret:  strings.TrimSpace(gitlabToken),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("POST /api/v1/tasks", h.createTask)
	mux.HandleFunc("GET /api/v1/tasks", h.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.getTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", h.listTaskEvents)
	mux.HandleFunc("POST /api/v1/tasks/{id}/workspace", h.createWorkspace)
	mux.HandleFunc("POST /api/v1/tasks/{id}/workspace/prepare", h.prepareWorkspace)
	mux.HandleFunc("GET /api/v1/tasks/{id}/workspace", h.getWorkspace)
	mux.HandleFunc("GET /api/v1/tasks/{id}/workspace/diff", h.getWorkspaceDiff)
	mux.HandleFunc("POST /api/v1/tasks/{id}/artifacts/diff", h.archiveWorkspaceDiff)
	mux.HandleFunc("GET /api/v1/artifacts/{id}", h.getArtifact)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/content", h.getArtifactContent)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", h.updateTask)

	return mux
}

func (h *handler) getArtifactContent(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	metadata, content, ok := h.artifacts.GetContent(r.PathValue("id"), tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Artifact not found")
		return
	}
	w.Header().Set("Content-Type", metadata.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.SizeBytes, 10))
	w.Header().Set("ETag", `"`+metadata.SHA256+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (h *handler) getArtifact(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	metadata, ok := h.artifacts.Get(r.PathValue("id"), tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Artifact not found")
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (h *handler) archiveWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	var request archiveDiffRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	if exceedsIdentifierLimit(request.RequestID, request.IdempotencyKey, request.TenantID) {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return
	}
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" || request.ExpectedWorkspaceVersion == 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey, tenantId and expectedWorkspaceVersion are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.reviews.Archive(r.Context(), review.ArchiveInput{
		RequestID:                request.RequestID,
		TaskID:                   r.PathValue("id"),
		TenantID:                 request.TenantID,
		IdempotencyKey:           request.IdempotencyKey,
		ExpectedWorkspaceVersion: request.ExpectedWorkspaceVersion,
	})
	if errors.Is(err, review.ErrWorkspaceNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if errors.Is(err, review.ErrWorkspaceNotReady) {
		writeError(w, http.StatusConflict, "workspace_not_ready", "workspace must be READY before archiving its diff")
		return
	}
	if errors.Is(err, workspace.ErrVersionConflict) {
		writeError(w, http.StatusConflict, "version_conflict", "workspace version does not match expectedWorkspaceVersion")
		return
	}
	if errors.Is(err, artifact.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different Artifact content")
		return
	}
	if errors.Is(err, repository.ErrDiffTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "diff_too_large", "repository diff exceeds the supported size")
		return
	}
	if errors.Is(err, repository.ErrDiffUnavailable) {
		h.logServerError(r, request.RequestID, request.TenantID, "diff_unavailable", err)
		writeError(w, http.StatusServiceUnavailable, "diff_unavailable", "repository diff is unavailable")
		return
	}
	if err != nil {
		h.logServerError(r, request.RequestID, request.TenantID, "internal_error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "repository diff could not be archived")
		return
	}

	w.Header().Set("Location", "/api/v1/artifacts/"+result.Artifact.ID)
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Artifact)
}

func (h *handler) getWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	diff, err := h.reviews.Get(r.Context(), review.GetInput{
		TaskID:   r.PathValue("id"),
		TenantID: tenantID,
	})
	if errors.Is(err, review.ErrWorkspaceNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if errors.Is(err, review.ErrWorkspaceNotReady) {
		writeError(w, http.StatusConflict, "workspace_not_ready", "workspace must be READY before reading its diff")
		return
	}
	if errors.Is(err, repository.ErrDiffTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "diff_too_large", "repository diff exceeds the supported size")
		return
	}
	if errors.Is(err, repository.ErrDiffUnavailable) {
		h.logServerError(r, r.Header.Get("X-Request-ID"), tenantID, "diff_unavailable", err)
		writeError(w, http.StatusServiceUnavailable, "diff_unavailable", "repository diff is unavailable")
		return
	}
	if err != nil {
		h.logServerError(r, r.Header.Get("X-Request-ID"), tenantID, "internal_error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "repository diff is unavailable")
		return
	}

	writeJSON(w, http.StatusOK, diff)
}

func (h *handler) prepareWorkspace(w http.ResponseWriter, r *http.Request) {
	var request prepareWorkspaceRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	if exceedsIdentifierLimit(request.RequestID, request.IdempotencyKey, request.TenantID) {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return
	}
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" || request.ExpectedVersion == 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey, tenantId and expectedVersion are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.workspaces.Prepare(r.Context(), workspace.PrepareInput{
		RequestID:       request.RequestID,
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
		h.logServerError(r, request.RequestID, request.TenantID, "workspace_preparation_unavailable", err)
		writeError(w, http.StatusServiceUnavailable, "workspace_preparation_unavailable", "workspace preparation is unavailable")
		return
	}
	if errors.Is(err, workspace.ErrPreparationFailed) {
		h.logServerError(r, request.RequestID, request.TenantID, "workspace_preparation_failed", err)
		writeError(w, http.StatusServiceUnavailable, "workspace_preparation_failed", "workspace preparation failed")
		return
	}
	if err != nil {
		h.logServerError(r, request.RequestID, request.TenantID, "internal_error", err)
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
	if !decodeRequest(w, r, &request) {
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	if exceedsIdentifierLimit(request.RequestID, request.IdempotencyKey, request.TenantID) {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return
	}
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	result, err := h.workspaces.Register(r.Context(), workspace.RegisterInput{
		RequestID:      request.RequestID,
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
		h.logServerError(r, request.RequestID, request.TenantID, "repository_verification_unavailable", err)
		writeError(w, http.StatusServiceUnavailable, "repository_verification_unavailable", "repository verification is unavailable")
		return
	}
	if err != nil {
		h.logServerError(r, request.RequestID, request.TenantID, "internal_error", err)
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
	if !decodeRequest(w, r, &request) {
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.Status = task.Status(strings.TrimSpace(string(request.Status)))
	if exceedsIdentifierLimit(request.RequestID, request.IdempotencyKey, request.TenantID) {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return
	}
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
		h.logServerError(r, request.RequestID, request.TenantID, "internal_error", err)
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

func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	writeJSON(w, http.StatusOK, listTasksResponse{Items: h.tasks.List(tenantID)})
}

func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenantId"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "tenantId is required")
		return
	}

	found, ok := h.tasks.Get(r.PathValue("id"), tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}

	writeJSON(w, http.StatusOK, found)
}

func (h *handler) createTask(w http.ResponseWriter, r *http.Request) {
	var request createTaskRequest
	if !decodeRequest(w, r, &request) {
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
	request.Repository.TargetBranch = strings.TrimSpace(request.Repository.TargetBranch)
	request.Repository.TargetSHA = strings.TrimSpace(request.Repository.TargetSHA)
	// len 按 UTF-8 字节计数；限制这些会进入内存索引或响应的字段，避免小请求体
	// 通过少数巨大字段反复占用内存。SHA 在下方还有更严格的固定长度校验。
	if exceedsIdentifierLimit(request.RequestID, request.IdempotencyKey, request.TenantID, request.Type, request.Repository.RepositoryID) || len(request.Goal) > maxGoalBytes {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return
	}
	if request.RequestID == "" || request.IdempotencyKey == "" || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "requestId, idempotencyKey and tenantId are required")
		return
	}
	if request.Type == "" || request.Goal == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "type and goal are required")
		return
	}
	if request.Repository.Provider == "" || request.Repository.RepositoryID == "" || request.Repository.HeadSHA == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "repository provider, repositoryId and headSha are required")
		return
	}
	if request.Repository.Provider != "gitlab" {
		writeError(w, http.StatusBadRequest, "validation_error", "repository provider must be gitlab")
		return
	}
	if !isValidGitLabRepositoryID(request.Repository.RepositoryID) {
		writeError(w, http.StatusBadRequest, "validation_error", "repositoryId must be a numeric project ID or a namespace/path")
		return
	}
	if !isGitObjectID(request.Repository.HeadSHA) || (request.Repository.BaseSHA != "" && !isGitObjectID(request.Repository.BaseSHA)) {
		writeError(w, http.StatusBadRequest, "validation_error", "headSha and any legacy baseSha must be 40 or 64 hexadecimal characters")
		return
	}
	if request.Repository.TargetBranch != "" || request.Repository.TargetSHA != "" {
		writeError(w, http.StatusBadRequest, "validation_error", "targetBranch and targetSha are assigned by the server")
		return
	}

	w.Header().Set("X-Request-ID", request.RequestID)
	// 旧客户端传来的 baseSha 只为过渡兼容而接受；评审基线始终由服务端重新计算。
	selection := task.RepositoryReference{
		Provider:     request.Repository.Provider,
		RepositoryID: request.Repository.RepositoryID,
		HeadSHA:      request.Repository.HeadSHA,
	}
	createInput := task.CreateInput{
		RequestID:      request.RequestID,
		TenantID:       request.TenantID,
		IdempotencyKey: request.IdempotencyKey,
		Type:           request.Type,
		Goal:           request.Goal,
		Repository:     selection,
	}
	// 先看幂等记录，避免重试时 master 已前进却重新计算出另一份 Task 输入。
	if replay, found, replayErr := h.tasks.ReplayCreate(createInput); found {
		if errors.Is(replayErr, task.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different task content")
			return
		}
		w.Header().Set("Location", "/api/v1/tasks/"+replay.Task.ID+"?tenantId="+url.QueryEscape(replay.Task.TenantID))
		writeJSON(w, http.StatusOK, replay.Task)
		return
	}
	resolved, err := h.resolver.Resolve(r.Context(), selection)
	if errors.Is(err, repository.ErrRepositoryNotFound) || errors.Is(err, repository.ErrHeadCommitNotFound) || errors.Is(err, repository.ErrTargetBranchNotFound) {
		writeError(w, http.StatusNotFound, "repository_reference_not_found", "repository, head commit or master branch was not found")
		return
	}
	if errors.Is(err, repository.ErrReviewBaseNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "review_base_not_found", "master and head commit have no available common ancestor")
		return
	}
	if err != nil {
		h.logServerError(r, request.RequestID, request.TenantID, "repository_resolution_unavailable", err)
		writeError(w, http.StatusServiceUnavailable, "repository_resolution_unavailable", "repository reference could not be resolved")
		return
	}
	if resolved.Provider != selection.Provider || resolved.RepositoryID != selection.RepositoryID || resolved.HeadSHA != selection.HeadSHA || resolved.TargetBranch != task.ReviewTargetBranch || !isGitObjectID(resolved.TargetSHA) || !isGitObjectID(resolved.BaseSHA) {
		h.logServerError(r, request.RequestID, request.TenantID, "repository_resolution_unavailable", errors.New("resolver returned an invalid review reference"))
		writeError(w, http.StatusServiceUnavailable, "repository_resolution_unavailable", "repository reference could not be resolved")
		return
	}
	createInput.Repository = resolved
	result, err := h.tasks.Create(createInput)
	if errors.Is(err, task.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used with different task content")
		return
	}
	if err != nil {
		h.logServerError(r, request.RequestID, request.TenantID, "internal_error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "task creation failed")
		return
	}
	w.Header().Set("Location", "/api/v1/tasks/"+result.Task.ID+"?tenantId="+url.QueryEscape(result.Task.TenantID))
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Task)
}

// GitLab 项目 API 接受数字 ID 或 namespace/project 路径。像 Java Controller 的参数校验一样，
// 先在 HTTP 边界拒绝畸形路径，再由 GitLab 验证仓库是否真的存在；两种校验职责不同。
func isValidGitLabRepositoryID(id string) bool {
	if id == "" || len(id) > maxIdentifierBytes || strings.Contains(id, "..") {
		return false
	}

	allDigits := true
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}

	// 非数字 ID 必须至少有 namespace/project 两段；空段和 "." 段会让路径含义不明确。
	if !strings.Contains(id, "/") {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			ch := segment[i]
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
				continue
			}
			return false
		}
	}
	return true
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

func exceedsIdentifierLimit(values ...string) bool {
	for _, value := range values {
		if len(value) > maxIdentifierBytes {
			return true
		}
	}
	return false
}

func decodeRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	// MaxBytesReader 限制实际读取量，不依赖客户端可能伪造或省略的 Content-Length。
	// 所有写接口共用此入口，避免某条路由漏掉限制。
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		writeDecodeError(w, err)
		return false
	}
	// 第二次 Decode 必须读到 EOF：既拒绝第二份 JSON，也使尾随空白计入 64 KiB。
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeDecodeError(w, err)
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeError(w, http.StatusBadRequest, "validation_error", "request body must not exceed 64 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
}

func (h *handler) logServerError(r *http.Request, requestID, tenantID, code string, err error) {
	// 仅记录定位故障需要的坐标和错误链，不记录 request body、幂等键或凭据。
	// token 不应进入 Git URL/argv；但 Git stderr 属于外部文本，仍对构造时传入的 token
	// 做兜底遮盖，避免远端异常回显时把凭据带进日志。
	redact := func(value string) string {
		if h.logSecret == "" {
			return value
		}
		return strings.ReplaceAll(value, h.logSecret, "[REDACTED]")
	}
	boundedField := func(value string) string {
		value = redact(value)
		if len(value) > maxIdentifierBytes {
			return "[overlong]"
		}
		return value
	}
	slog.ErrorContext(r.Context(), "API request failed",
		"requestId", boundedField(requestID),
		"tenantId", boundedField(tenantID),
		"taskId", boundedField(r.PathValue("id")),
		"errorCode", code,
		"cause", redact(fmt.Sprintf("%v", err)),
	)
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
