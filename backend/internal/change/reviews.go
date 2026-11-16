package change

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"time"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

var (
	ErrSnapshotNotReady = errors.New("review requires a READY snapshot belonging to this change")
	ErrReviewComplete   = errors.New("all services in this snapshot have already succeeded")
)

type ReviewInput struct {
	IdempotencyKey  string `json:"idempotencyKey"`
	ExpectedVersion uint64 `json:"expectedVersion"`
	SnapshotID      string `json:"snapshotId"`
}
type ServiceReview struct {
	ServiceID        string `json:"serviceId"`
	State            string `json:"state"`
	Stage            string `json:"stage"`
	TaskID           string `json:"taskId,omitempty"`
	WorkspaceID      string `json:"workspaceId,omitempty"`
	WorkspaceVersion uint64 `json:"workspaceVersion,omitempty"`
	ExecutionID      string `json:"executionId,omitempty"`
	ArtifactID       string `json:"artifactId,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
	Reused           bool   `json:"reused,omitempty"`
}
type ReviewRun struct {
	ID           string          `json:"id"`
	SnapshotID   string          `json:"snapshotId"`
	SnapshotHash string          `json:"snapshotHash"`
	CreatedBy    string          `json:"createdBy"`
	State        string          `json:"state"`
	Services     []ServiceReview `json:"services"`
	StartedAt    time.Time       `json:"startedAt"`
	CompletedAt  time.Time       `json:"completedAt,omitzero"`
}
type ReviewResult struct {
	ChangeID      string    `json:"changeId"`
	ChangeVersion uint64    `json:"changeVersion"`
	Review        ReviewRun `json:"review"`
}
type reviewOperation struct {
	key, hash string
	done      chan struct{}
	result    ReviewResult
}

// NewServiceWithReviews shares all execution stores with the original workbench.
func NewServiceWithReviews(catalog *servicecatalog.Catalog, resolver repository.BranchResolver, tasks *task.Store, workspaces *workspace.Manager, reviews *review.Service) *Service {
	s := NewService(catalog, resolver)
	s.tasks, s.workspaces, s.reviews = tasks, workspaces, reviews
	return s
}

func (s *Service) ListReviews(actor servicecatalog.Actor, id string) ([]ReviewRun, error) {
	if _, err := s.Get(actor, id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []ReviewRun{}
	for _, op := range s.records[id].reviews {
		values = append(values, cloneReview(op.result.Review))
	}
	return values, nil
}
func (s *Service) GetReview(actor servicecatalog.Actor, id, runID string) (ReviewRun, error) {
	values, err := s.ListReviews(actor, id)
	if err != nil {
		return ReviewRun{}, err
	}
	for _, value := range values {
		if value.ID == runID {
			return value, nil
		}
	}
	return ReviewRun{}, ErrNotFound
}

// Review is synchronous. A new key retries only incomplete services of the same
// snapshot. A replay waits for the original operation and never starts work.
func (s *Service) Review(ctx context.Context, actor servicecatalog.Actor, id string, input ReviewInput) (ReviewResult, bool, error) {
	if !validKey(input.IdempotencyKey) || input.ExpectedVersion == 0 || !validKey(input.SnapshotID) {
		return ReviewResult{}, false, ErrValidation
	}
	current, err := s.Get(actor, id)
	if err != nil {
		return ReviewResult{}, false, err
	}
	if _, _, err := s.authorize(actor, current.Environment, "review", current.Services); err != nil {
		return ReviewResult{}, false, err
	}
	var snapshot Snapshot
	for _, item := range current.Snapshots {
		if item.ID == input.SnapshotID {
			snapshot = item
			break
		}
	}
	if snapshot.State != "READY" {
		return ReviewResult{}, false, ErrSnapshotNotReady
	}
	if !s.reviews.Configured() {
		return ReviewResult{}, false, review.ErrReviewUnavailable
	}
	digest := hash(input)
	s.mu.Lock()
	r := s.records[id]
	var previous ReviewRun
	for _, prior := range r.reviews {
		if prior.key == input.IdempotencyKey {
			s.mu.Unlock()
			if prior.hash != digest {
				return ReviewResult{}, false, ErrIdempotencyConflict
			}
			select {
			case <-ctx.Done():
				return ReviewResult{}, false, ctx.Err()
			case <-prior.done:
				return cloneReviewResult(prior.result), true, nil
			}
		}
		if prior.result.Review.SnapshotID == snapshot.ID {
			previous = prior.result.Review
		}
	}
	if r.value.Version != input.ExpectedVersion {
		s.mu.Unlock()
		return ReviewResult{}, false, ErrVersionConflict
	}
	if r.busy {
		s.mu.Unlock()
		return ReviewResult{}, false, ErrInProgress
	}
	if previous.State == "SUCCEEDED" {
		s.mu.Unlock()
		return ReviewResult{}, false, ErrReviewComplete
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return ReviewResult{}, false, err
	}
	run := ReviewRun{ID: fmt.Sprintf("change-review-%d", len(r.reviews)+1), SnapshotID: snapshot.ID, SnapshotHash: snapshot.InputHash, CreatedBy: actor.Subject, State: "RUNNING", StartedAt: time.Now().UTC(), Services: []ServiceReview{}}
	for i, item := range snapshot.Services {
		entry := ServiceReview{ServiceID: item.ServiceID, State: "NOT_STARTED", Stage: "admission"}
		if len(previous.Services) > i {
			prior := previous.Services[i]
			entry.TaskID = prior.TaskID
			if prior.State == "SUCCEEDED" {
				entry = prior
				entry.Reused = true
			}
		}
		run.Services = append(run.Services, entry)
	}
	r.busy = true
	r.value.Version++
	op := &reviewOperation{key: input.IdempotencyKey, hash: digest, done: make(chan struct{}), result: ReviewResult{ChangeID: id, ChangeVersion: r.value.Version, Review: cloneReview(run)}}
	r.reviews = append(r.reviews, op)
	s.mu.Unlock()

	for i, item := range run.Services {
		if item.State == "SUCCEEDED" {
			continue
		}
		publish := func(value ServiceReview) {
			run.Services[i] = value
			s.mu.Lock()
			op.result.Review.Services[i] = value
			s.mu.Unlock()
		}
		if ctx.Err() != nil {
			item.State, item.ErrorCode = "CANCELED", "request_canceled"
			publish(item)
			continue
		}
		// Bounds admission, preparation and review together; the runner may impose
		// a tighter deployment timeout. Cancellation never starts the next service.
		serviceCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		s.reviewService(serviceCtx, current, snapshot, run.ID, i, item, publish)
		cancel()
	}
	run.State = "SUCCEEDED"
	succeeded := 0
	for _, item := range run.Services {
		if item.State == "SUCCEEDED" {
			succeeded++
		}
	}
	if succeeded < len(run.Services) {
		run.State = "FAILED"
		if succeeded > 0 {
			run.State = "PARTIAL"
		}
		if ctx.Err() != nil {
			run.State = "CANCELED"
		}
	}
	run.CompletedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	r.value.Version++
	r.busy = false
	op.result = ReviewResult{ChangeID: id, ChangeVersion: r.value.Version, Review: cloneReview(run)}
	close(op.done)
	return cloneReviewResult(op.result), false, nil
}

func (s *Service) reviewService(ctx context.Context, current ChangeRequest, snapshot Snapshot, runID string, index int, item ServiceReview, publish func(ServiceReview)) {
	item.State = "RUNNING"
	publish(item)
	requestID := current.ID + ":" + runID + ":" + item.ServiceID
	fail := func(code string) {
		if ws, ok := s.workspaces.Get(item.TaskID, current.TenantID); ok {
			item.WorkspaceID, item.WorkspaceVersion = ws.ID, ws.Version
		}
		item.State, item.ErrorCode = "FAILED", code
		if ctx.Err() != nil {
			item.State, item.ErrorCode = "CANCELED", "request_canceled"
		}
		publish(item)
	}
	if item.TaskID == "" {
		created, err := s.tasks.Create(task.CreateInput{RequestID: requestID, TenantID: current.TenantID,
			IdempotencyKey: current.ID + ":" + snapshot.ID + ":" + item.ServiceID, Type: "PR_REVIEW", Goal: current.Title,
			Repository: *snapshot.Services[index].Repository, Change: task.ChangeReference{ChangeID: current.ID, SnapshotID: snapshot.ID, ServiceID: item.ServiceID, Environment: current.Environment}})
		if err != nil {
			fail("task_creation_failed")
			return
		}
		item.TaskID = created.Task.ID
		publish(item)
	}
	child, _ := s.tasks.Get(item.TaskID, current.TenantID)
	if child.Status == task.StatusCreated {
		_, err := s.tasks.Transition(task.TransitionInput{RequestID: requestID, TenantID: current.TenantID, TaskID: child.ID, IdempotencyKey: rand.Text(), ExpectedVersion: child.Version, Status: task.StatusQueued})
		if err != nil {
			fail("task_admission_failed")
			return
		}
	}
	if ctx.Err() != nil {
		fail("request_canceled")
		return
	}
	ws, exists := s.workspaces.Get(item.TaskID, current.TenantID)
	if !exists {
		registered, err := s.workspaces.Register(ctx, workspace.RegisterInput{RequestID: requestID, TenantID: current.TenantID, TaskID: item.TaskID, IdempotencyKey: rand.Text()})
		if err != nil {
			fail("workspace_registration_failed")
			return
		}
		ws = registered.Workspace
	}
	item.WorkspaceID, item.WorkspaceVersion, item.Stage = ws.ID, ws.Version, "preparation"
	publish(item)
	if ctx.Err() != nil {
		fail("request_canceled")
		return
	}
	if ws.State != workspace.StateReady {
		prepared, err := s.workspaces.Prepare(ctx, workspace.PrepareInput{RequestID: requestID, TenantID: current.TenantID, TaskID: item.TaskID, IdempotencyKey: rand.Text(), ExpectedVersion: ws.Version})
		if err != nil {
			fail("workspace_preparation_failed")
			return
		}
		ws = prepared.Workspace
	}
	item.WorkspaceVersion, item.Stage = ws.Version, "review"
	publish(item)
	input := review.ExecuteInput{RequestID: requestID, TenantID: current.TenantID, TaskID: item.TaskID, IdempotencyKey: rand.Text(), ExpectedWorkspaceVersion: ws.Version}
	_, err := s.reviews.Execute(ctx, input)
	execution, ok := s.reviews.GetExecutionForRequest(input)
	if ok {
		item.ExecutionID = execution.ID
		if execution.Artifact != nil {
			item.ArtifactID = execution.Artifact.ID
		}
	}
	if err != nil {
		code := "review_execution_failed"
		if ok {
			code = execution.ErrorCode
		}
		fail(code)
		return
	}
	item.State, item.Stage = "SUCCEEDED", "complete"
	publish(item)
}
func cloneReview(value ReviewRun) ReviewRun {
	value.Services = slices.Clone(value.Services)
	return value
}
func cloneReviewResult(value ReviewResult) ReviewResult {
	value.Review = cloneReview(value.Review)
	return value
}
