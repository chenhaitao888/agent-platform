package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

var (
	ErrReviewUnavailable            = errors.New("review execution is not configured")
	ErrInvalidTaskType              = errors.New("review execution requires a PR_REVIEW task")
	ErrExecutionInProgress          = errors.New("review execution is in progress")
	ErrExecutionIdempotencyConflict = errors.New("idempotency key already used for another review execution")
)

type ExecutionState string

const (
	ExecutionRunning   ExecutionState = "RUNNING"
	ExecutionSucceeded ExecutionState = "SUCCEEDED"
	ExecutionFailed    ExecutionState = "FAILED"
	ExecutionCanceled  ExecutionState = "CANCELED"
)

type ExecuteInput struct {
	RequestID                string
	TenantID                 string
	TaskID                   string
	IdempotencyKey           string
	ExpectedWorkspaceVersion uint64
}

type Execution struct {
	ID               string             `json:"id"`
	TenantID         string             `json:"tenantId"`
	TaskID           string             `json:"taskId"`
	WorkspaceID      string             `json:"workspaceId"`
	WorkspaceVersion uint64             `json:"workspaceVersion"`
	BaseSHA          string             `json:"baseSha"`
	HeadSHA          string             `json:"headSha"`
	State            ExecutionState     `json:"state"`
	Artifact         *artifact.Artifact `json:"artifact,omitempty"`
	Runtime          *RuntimeIdentity   `json:"runtime,omitempty"`
	ErrorCode        string             `json:"errorCode,omitempty"`
	StartedAt        time.Time          `json:"startedAt"`
	CompletedAt      time.Time          `json:"completedAt,omitzero"`
}

type ExecuteResult struct {
	Execution Execution
	Created   bool
}

type executionScope struct{ tenantID, key string }
type executionRecord struct {
	taskID                   string
	expectedWorkspaceVersion uint64
	execution                Execution
	err                      error
}

func (s *Service) GetExecution(input GetInput) (Execution, bool) {
	if _, ok := s.tasks.Get(input.TaskID, input.TenantID); !ok {
		return Execution{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, ok := s.latest[input.TaskID]
	if !ok || current.TenantID != input.TenantID {
		return Execution{}, false
	}
	return current.clone(), true
}

func (s *Service) Execute(ctx context.Context, input ExecuteInput) (ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecuteResult{}, err
	}
	if input.RequestID == "" || input.TenantID == "" || input.TaskID == "" || input.IdempotencyKey == "" || input.ExpectedWorkspaceVersion == 0 {
		return ExecuteResult{}, ErrInvalidRunInput
	}
	currentTask, ok := s.tasks.Get(input.TaskID, input.TenantID)
	if !ok {
		return ExecuteResult{}, task.ErrTaskNotFound
	}
	scope := executionScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	s.mu.RLock()
	replay, done, replayErr := s.executionResultLocked(input)
	s.mu.RUnlock()
	if done {
		return replay, replayErr
	}
	if currentTask.Type != "PR_REVIEW" {
		return ExecuteResult{}, ErrInvalidTaskType
	}
	if currentTask.Status != task.StatusQueued {
		return ExecuteResult{}, workspace.ErrTaskNotQueued
	}
	current, ok := s.workspaces.Get(input.TaskID, input.TenantID)
	if !ok {
		return ExecuteResult{}, ErrWorkspaceNotFound
	}
	if current.State != workspace.StateReady || current.Path == "" {
		return ExecuteResult{}, ErrWorkspaceNotReady
	}
	if current.Version != input.ExpectedWorkspaceVersion {
		return ExecuteResult{}, workspace.ErrVersionConflict
	}
	if current.BaseSHA != currentTask.Repository.BaseSHA || current.HeadSHA != currentTask.Repository.HeadSHA || current.Repository.Provider != currentTask.Repository.Provider || current.Repository.RepositoryID != currentTask.Repository.RepositoryID {
		return ExecuteResult{}, ErrInvalidRunInput
	}
	if s.runner == nil {
		return ExecuteResult{}, ErrReviewUnavailable
	}
	s.mu.Lock()
	if replay, done, err := s.executionResultLocked(input); done {
		s.mu.Unlock()
		return replay, err
	}
	if active, ok := s.latest[input.TaskID]; ok && active.State == ExecutionRunning {
		s.mu.Unlock()
		return ExecuteResult{}, ErrExecutionInProgress
	}
	s.nextExecutionID++
	operation := Execution{ID: fmt.Sprintf("review-%d", s.nextExecutionID), TenantID: current.TenantID, TaskID: current.TaskID, WorkspaceID: current.ID, WorkspaceVersion: current.Version,
		BaseSHA: current.BaseSHA, HeadSHA: current.HeadSHA, State: ExecutionRunning, StartedAt: time.Now().UTC()}
	if profiler, ok := s.runner.(RuntimeProfiler); ok {
		identity := profiler.RuntimeIdentity()
		operation.Runtime = &identity
	}
	s.executions[scope] = executionRecord{taskID: input.TaskID, expectedWorkspaceVersion: input.ExpectedWorkspaceVersion, execution: operation}
	s.latest[input.TaskID] = operation
	s.appendReviewEvent(operation, task.EventTypeReviewStarted, input.RequestID, operation.StartedAt)
	s.mu.Unlock()

	patch, err := s.source.Read(ctx, repository.DiffInput{WorktreePath: current.Path, BaseSHA: current.BaseSHA, HeadSHA: current.HeadSHA})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return s.failExecution(scope, operation, input.RequestID, ctxErr)
	}
	if err != nil {
		if !errors.Is(err, repository.ErrDiffTooLarge) {
			err = repository.ErrDiffUnavailable
		}
		return s.failExecution(scope, operation, input.RequestID, err)
	}
	runInput := RunInput{WorktreePath: current.Path, BaseSHA: current.BaseSHA, HeadSHA: current.HeadSHA, Patch: string(patch)}
	if err := runInput.Validate(); err != nil {
		return s.failExecution(scope, operation, input.RequestID, err)
	}
	report, err := s.runner.Run(ctx, runInput)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return s.failExecution(scope, operation, input.RequestID, ctxErr)
	}
	if err != nil {
		return s.failExecution(scope, operation, input.RequestID, safeExecutionError(err))
	}
	content, err := json.Marshal(report)
	if err == nil {
		report, err = ParseFindings(content, current.BaseSHA, current.HeadSHA)
	}
	if err != nil {
		return s.failExecution(scope, operation, input.RequestID, ErrInvalidFindings)
	}
	content, err = json.Marshal(report)
	if err != nil {
		return s.failExecution(scope, operation, input.RequestID, ErrInvalidFindings)
	}
	if err := ctx.Err(); err != nil {
		return s.failExecution(scope, operation, input.RequestID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return s.failExecutionLocked(scope, operation, input.RequestID, err)
	}
	result, err := s.artifacts.Create(artifact.CreateInput{TenantID: current.TenantID, TaskID: current.TaskID, WorkspaceID: current.ID,
		IdempotencyKey: "review:" + operation.ID, IdempotencyNamespace: "review-execution", Type: artifact.TypePRReviewFindings, MediaType: "application/json", Content: content})
	if err != nil {
		return s.failExecutionLocked(scope, operation, input.RequestID, ErrExecutionFailed)
	}
	if result.Created {
		s.tasks.AppendEvent(task.AppendEventInput{TenantID: current.TenantID, TaskID: current.TaskID, EventType: task.EventTypeArtifactCreated, CausationID: input.RequestID, OccurredAt: result.Artifact.CreatedAt,
			Payload: task.EventPayload{Artifact: &task.ArtifactEventPayload{ArtifactID: result.Artifact.ID, WorkspaceID: current.ID, Type: string(result.Artifact.Type), MediaType: result.Artifact.MediaType, SHA256: result.Artifact.SHA256, SizeBytes: result.Artifact.SizeBytes}}})
	}
	operation.State = ExecutionSucceeded
	operation.Artifact = &result.Artifact
	operation.CompletedAt = time.Now().UTC()
	s.finishExecutionLocked(scope, operation, nil)
	s.appendReviewEvent(operation, task.EventTypeReviewSucceeded, input.RequestID, operation.CompletedAt)
	return ExecuteResult{Execution: operation.clone(), Created: true}, nil
}

func (s *Service) executionResultLocked(input ExecuteInput) (ExecuteResult, bool, error) {
	record, ok := s.executions[executionScope{tenantID: input.TenantID, key: input.IdempotencyKey}]
	if !ok {
		return ExecuteResult{}, false, nil
	}
	if record.taskID != input.TaskID || record.expectedWorkspaceVersion != input.ExpectedWorkspaceVersion {
		return ExecuteResult{}, true, ErrExecutionIdempotencyConflict
	}
	if record.execution.State == ExecutionRunning {
		return ExecuteResult{}, true, ErrExecutionInProgress
	}
	if record.err != nil {
		return ExecuteResult{}, true, record.err
	}
	return ExecuteResult{Execution: record.execution.clone()}, true, nil
}

func (s *Service) failExecution(scope executionScope, operation Execution, requestID string, err error) (ExecuteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failExecutionLocked(scope, operation, requestID, err)
}

func (s *Service) failExecutionLocked(scope executionScope, operation Execution, requestID string, err error) (ExecuteResult, error) {
	operation.State = ExecutionFailed
	eventType := task.EventTypeReviewFailed
	if errors.Is(err, context.Canceled) {
		operation.State = ExecutionCanceled
		eventType = task.EventTypeReviewCanceled
	}
	operation.ErrorCode = executionErrorCode(err)
	operation.CompletedAt = time.Now().UTC()
	s.finishExecutionLocked(scope, operation, err)
	s.appendReviewEvent(operation, eventType, requestID, operation.CompletedAt)
	return ExecuteResult{}, err
}

func (s *Service) finishExecutionLocked(scope executionScope, operation Execution, err error) {
	record := s.executions[scope]
	record.execution = operation
	record.err = err
	s.executions[scope] = record
	s.latest[operation.TaskID] = operation
}

func (s *Service) appendReviewEvent(operation Execution, eventType task.EventType, requestID string, occurredAt time.Time) {
	payload := task.ReviewEventPayload{ExecutionID: operation.ID, WorkspaceID: operation.WorkspaceID, WorkspaceVersion: operation.WorkspaceVersion, State: string(operation.State), ErrorCode: operation.ErrorCode}
	if operation.Artifact != nil {
		payload.ArtifactID = operation.Artifact.ID
	}
	s.tasks.AppendEvent(task.AppendEventInput{TenantID: operation.TenantID, TaskID: operation.TaskID, EventType: eventType, CausationID: requestID, OccurredAt: occurredAt, Payload: task.EventPayload{Review: &payload}})
}

func (operation Execution) clone() Execution {
	if operation.Runtime != nil {
		identity := *operation.Runtime
		operation.Runtime = &identity
	}
	if operation.Artifact != nil {
		metadata := *operation.Artifact
		operation.Artifact = &metadata
	}
	return operation
}

func safeExecutionError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrReviewUnavailable, ErrInvalidRunInput, ErrInvalidFindings, ErrOutputUnavailable, ErrOutputTooLarge} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrExecutionFailed
}

func executionErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "review_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "review_timeout"
	case errors.Is(err, ErrInvalidFindings):
		return "review_findings_invalid"
	case errors.Is(err, ErrInvalidRunInput):
		return "review_input_invalid"
	case errors.Is(err, ErrOutputUnavailable):
		return "review_output_unavailable"
	case errors.Is(err, ErrOutputTooLarge):
		return "review_output_too_large"
	case errors.Is(err, repository.ErrDiffTooLarge):
		return "diff_too_large"
	case errors.Is(err, repository.ErrDiffUnavailable):
		return "diff_unavailable"
	case errors.Is(err, ErrReviewUnavailable):
		return "review_unavailable"
	default:
		return "review_execution_failed"
	}
}
