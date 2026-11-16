package change

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/workspace"
)

var ErrPlanningUnavailable = errors.New("planning connectors are not configured")

type PlanInput struct {
	IdempotencyKey  string                 `json:"idempotencyKey"`
	ExpectedVersion uint64                 `json:"expectedVersion"`
	SnapshotID      string                 `json:"snapshotId"`
	Requirements    []planning.Requirement `json:"requirements,omitempty"`
}
type PlanResult struct {
	ChangeID      string        `json:"changeId"`
	ChangeVersion uint64        `json:"changeVersion"`
	Plan          planning.Plan `json:"plan"`
}
type planOperation struct {
	key, hash string
	done      chan struct{}
	result    PlanResult
}

// ConfigurePlanning is used only during assembly, before publishing the handler.
func (s *Service) ConfigurePlanning(p *planning.Planner) { s.planner = p }
func (s *Service) ListPlans(actor servicecatalog.Actor, id string) ([]planning.Plan, error) {
	if _, err := s.Get(actor, id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []planning.Plan{}
	for _, op := range s.records[id].plans {
		values = append(values, planning.Clone(op.result.Plan))
	}
	return values, nil
}
func (s *Service) GetPlan(actor servicecatalog.Actor, id, planID string) (planning.Plan, error) {
	values, err := s.ListPlans(actor, id)
	if err != nil {
		return planning.Plan{}, err
	}
	for _, value := range values {
		if value.ID == planID {
			return value, nil
		}
	}
	return planning.Plan{}, ErrNotFound
}
func (s *Service) Plan(ctx context.Context, actor servicecatalog.Actor, id string, input PlanInput) (PlanResult, bool, error) {
	if !validKey(input.IdempotencyKey) || input.ExpectedVersion == 0 || !validKey(input.SnapshotID) {
		return PlanResult{}, false, ErrValidation
	}
	current, err := s.Get(actor, id)
	if err != nil {
		return PlanResult{}, false, err
	}
	_, entries, err := s.authorize(actor, current.Environment, "plan", current.Services)
	if err != nil {
		return PlanResult{}, false, err
	}
	ids := []string{}
	for _, item := range current.Services {
		ids = append(ids, item.ServiceID)
	}
	input.Requirements, err = planning.Normalize(input.Requirements, ids)
	if err != nil {
		return PlanResult{}, false, ErrValidation
	}
	var snapshot Snapshot
	for _, item := range current.Snapshots {
		if item.ID == input.SnapshotID {
			snapshot = item
			break
		}
	}
	if snapshot.State != "READY" {
		return PlanResult{}, false, ErrSnapshotNotReady
	}
	if s.planner == nil {
		return PlanResult{}, false, ErrPlanningUnavailable
	}
	digest := hash(input)
	s.mu.Lock()
	r := s.records[id]
	for _, prior := range r.plans {
		if prior.key == input.IdempotencyKey {
			s.mu.Unlock()
			if prior.hash != digest {
				return PlanResult{}, false, ErrIdempotencyConflict
			}
			select {
			case <-ctx.Done():
				return PlanResult{}, false, ctx.Err()
			case <-prior.done:
				return planning.Clone(prior.result), true, nil
			}
		}
	}
	if r.value.Version != input.ExpectedVersion {
		s.mu.Unlock()
		return PlanResult{}, false, ErrVersionConflict
	}
	if r.busy {
		s.mu.Unlock()
		return PlanResult{}, false, ErrInProgress
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return PlanResult{}, false, ctx.Err()
	}
	var evidence ReviewRun
	for _, op := range r.reviews {
		if op.result.Review.SnapshotID == snapshot.ID {
			evidence = cloneReview(op.result.Review)
		}
	}
	r.busy = true
	r.value.Version++
	revision := uint64(len(r.plans) + 1)
	planID := fmt.Sprintf("plan-%d", revision)
	op := &planOperation{key: input.IdempotencyKey, hash: digest, done: make(chan struct{}), result: PlanResult{ChangeID: id, ChangeVersion: r.value.Version, Plan: planning.Plan{ID: planID, Revision: revision, SchemaVersion: "change-plan.v1", TenantID: actor.TenantID, Environment: current.Environment, CreatedBy: actor.Subject, CreatedAt: time.Now().UTC(), SnapshotID: snapshot.ID, SnapshotHash: snapshot.InputHash, State: "RUNNING", Services: []planning.Source{}, Actions: []planning.Action{}, Issues: []planning.Issue{}, Observations: []planning.Observation{}}}}
	r.plans = append(r.plans, op)
	s.mu.Unlock()
	build := planning.Input{TenantID: actor.TenantID, Environment: current.Environment, SnapshotID: snapshot.ID, SnapshotHash: snapshot.InputHash, CreatedBy: actor.Subject, Services: []planning.ServiceInput{}}
	for i, item := range snapshot.Services {
		source := planning.Source{ServiceID: item.ServiceID, CatalogVersion: item.CatalogVersion, HeadSHA: item.Repository.HeadSHA, BaseSHA: item.Repository.BaseSHA, Review: planning.ReviewEvidence{State: "NOT_STARTED"}}
		for _, prior := range evidence.Services {
			if prior.ServiceID == item.ServiceID {
				source.Review = planning.ReviewEvidence{TaskID: prior.TaskID, ExecutionID: prior.ExecutionID, ArtifactID: prior.ArtifactID, State: prior.State}
				break
			}
		}
		service := planning.ServiceInput{Source: source}
		for _, binding := range entries[i].Planning {
			if binding.Environment == current.Environment {
				service.Bindings = binding
				break
			}
		}
		for _, requirement := range input.Requirements {
			if requirement.ServiceID == item.ServiceID {
				service.Requirement = requirement
				break
			}
		}
		service.Patch, service.AnalysisError = s.planPatch(ctx, current.ID, snapshot.ID, current.TenantID, item, source.Review.TaskID)
		build.Services = append(build.Services, service)
	}
	plan := s.planner.Build(ctx, build)
	plan.ID, plan.Revision = planID, revision
	plan.Hash = planning.Hash(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	r.value.Version++
	r.busy = false
	op.result = PlanResult{ChangeID: id, ChangeVersion: r.value.Version, Plan: plan}
	close(op.done)
	return planning.Clone(op.result), false, nil
}
func (s *Service) planPatch(ctx context.Context, changeID, snapshotID, tenantID string, item ServiceSnapshot, taskID string) (string, string) {
	if ctx.Err() != nil {
		return "", "analysis_canceled"
	}
	child, ok := s.tasks.Get(taskID, tenantID)
	if !ok || child.Change.ChangeID != changeID || child.Change.SnapshotID != snapshotID || child.Change.ServiceID != item.ServiceID || child.Repository != *item.Repository {
		return "", "workspace_required"
	}
	ws, ok := s.workspaces.Get(taskID, tenantID)
	if !ok || ws.State != workspace.StateReady {
		return "", "workspace_required"
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	diff, err := s.reviews.Get(readCtx, review.GetInput{TaskID: taskID, TenantID: tenantID})
	if err != nil || diff.TaskID != taskID || diff.WorkspaceID != ws.ID || diff.HeadSHA != item.Repository.HeadSHA || diff.BaseSHA != item.Repository.BaseSHA {
		return "", "diff_unavailable"
	}
	return diff.Patch, ""
}
