// Package change owns synchronous, in-memory change snapshots and their reviews.
// HTTP only adapts requests; branch resolution and review execution are explicit.
package change

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

var (
	ErrValidation          = errors.New("invalid change input")
	ErrForbidden           = errors.New("service, repository, environment or operation is not authorized")
	ErrNotFound            = errors.New("change not found")
	ErrIdempotencyConflict = errors.New("idempotency key already used with different content")
	ErrVersionConflict     = errors.New("change version does not match expectedVersion")
	ErrInProgress          = errors.New("another snapshot or review is running for this change")
)

type Selection struct {
	ServiceID    string `json:"serviceId"`
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch,omitempty"`
}
type CreateInput struct {
	IdempotencyKey string      `json:"idempotencyKey"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Environment    string      `json:"environment"`
	Services       []Selection `json:"services"`
}
type SnapshotInput struct {
	IdempotencyKey  string      `json:"idempotencyKey"`
	ExpectedVersion uint64      `json:"expectedVersion"`
	Services        []Selection `json:"services"`
}
type ChangeRequest struct {
	ID                string      `json:"id"`
	TenantID          string      `json:"tenantId"`
	CreatedBy         string      `json:"createdBy"`
	Title             string      `json:"title"`
	Description       string      `json:"description"`
	Environment       string      `json:"environment"`
	Services          []Selection `json:"services"`
	Version           uint64      `json:"version"`
	CurrentSnapshotID string      `json:"currentSnapshotId,omitempty"`
	Snapshots         []Snapshot  `json:"snapshots"`
	CreatedAt         time.Time   `json:"createdAt"`
}
type ServiceSnapshot struct {
	ServiceID      string                    `json:"serviceId"`
	CatalogVersion string                    `json:"catalogVersion"`
	ConnectorRef   string                    `json:"connectorRef"`
	SourceBranch   string                    `json:"sourceBranch"`
	TargetBranch   string                    `json:"targetBranch"`
	Repository     *task.RepositoryReference `json:"repository,omitempty"`
	ResolvedAt     time.Time                 `json:"resolvedAt"`
	ErrorCode      string                    `json:"errorCode,omitempty"`
}
type Snapshot struct {
	ID        string            `json:"id"`
	Revision  uint64            `json:"revision"`
	State     string            `json:"state"`
	InputHash string            `json:"inputHash"`
	CreatedBy string            `json:"createdBy"`
	Services  []ServiceSnapshot `json:"services"`
	CreatedAt time.Time         `json:"createdAt"`
}
type SnapshotResult struct {
	ChangeID      string   `json:"changeId"`
	ChangeVersion uint64   `json:"changeVersion"`
	Snapshot      Snapshot `json:"snapshot"`
}
type creation struct {
	hash   string
	result ChangeRequest
}
type operation struct {
	hash   string
	done   chan struct{}
	result SnapshotResult
}
type record struct {
	value      ChangeRequest
	operations map[string]*operation
	reviews    []*reviewOperation
	busy       bool
}
type Service struct {
	mu         sync.Mutex
	catalog    *servicecatalog.Catalog
	resolver   repository.BranchResolver
	records    map[string]*record
	creations  map[[2]string]creation
	tasks      *task.Store
	workspaces *workspace.Manager
	reviews    *review.Service
}

func NewService(catalog *servicecatalog.Catalog, resolver repository.BranchResolver) *Service {
	return &Service{catalog: catalog, resolver: resolver, records: map[string]*record{}, creations: map[[2]string]creation{}}
}
func (s *Service) Services(actor servicecatalog.Actor) []servicecatalog.PublicEntry {
	return s.catalog.List(actor)
}
func validKey(key string) bool { return strings.TrimSpace(key) == key && key != "" && len(key) <= 256 }
func hash(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// authorize resolves only deployment-owned mappings, for ALL selections, before
// the first external request. Input order has no significance for idempotency.
func (s *Service) authorize(actor servicecatalog.Actor, env, operation string, selections []Selection) ([]Selection, []servicecatalog.Entry, error) {
	if len(selections) == 0 || len(selections) > 20 {
		return nil, nil, ErrValidation
	}
	selections = slices.Clone(selections)
	slices.SortFunc(selections, func(a, b Selection) int { return strings.Compare(a.ServiceID, b.ServiceID) })
	entries := make([]servicecatalog.Entry, 0, len(selections))
	for i, item := range selections {
		if item.ServiceID == "" || !repository.ValidBranch(item.SourceBranch) || (item.TargetBranch != "" && !repository.ValidBranch(item.TargetBranch)) || (i > 0 && item.ServiceID == selections[i-1].ServiceID) {
			return nil, nil, ErrValidation
		}
		entry, ok := s.catalog.Authorized(actor, item.ServiceID, env, operation)
		if !ok {
			return nil, nil, ErrForbidden
		}
		if item.TargetBranch == "" {
			selections[i].TargetBranch = entry.Repository.DefaultTargetBranch
		}
		if !slices.Contains(entry.Repository.AllowedTargetBranches, selections[i].TargetBranch) {
			return nil, nil, ErrForbidden
		}
		entries = append(entries, entry)
	}
	return selections, entries, nil
}
func (s *Service) Create(actor servicecatalog.Actor, input CreateInput) (ChangeRequest, bool, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if !validKey(input.IdempotencyKey) || input.Title == "" || len(input.Title) > 256 || len(input.Description) > 4096 {
		return ChangeRequest{}, false, ErrValidation
	}
	selections, _, err := s.authorize(actor, input.Environment, "create", input.Services)
	if err != nil {
		return ChangeRequest{}, false, err
	}
	input.Services = selections
	digest := hash(input)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]string{actor.TenantID, input.IdempotencyKey}
	if prior, ok := s.creations[key]; ok {
		if prior.hash != digest {
			return ChangeRequest{}, false, ErrIdempotencyConflict
		}
		return clone(prior.result), true, nil
	}
	value := ChangeRequest{ID: fmt.Sprintf("change-%d", len(s.records)+1), TenantID: actor.TenantID, CreatedBy: actor.Subject, Title: input.Title, Description: input.Description, Environment: input.Environment, Services: selections, Version: 1, Snapshots: []Snapshot{}, CreatedAt: time.Now().UTC()}
	s.records[value.ID] = &record{value: value, operations: map[string]*operation{}}
	s.creations[key] = creation{hash: digest, result: clone(value)}
	return clone(value), false, nil
}
func (s *Service) canRead(actor servicecatalog.Actor, value ChangeRequest) bool {
	for _, item := range value.Services {
		if _, ok := s.catalog.Authorized(actor, item.ServiceID, value.Environment, "read"); !ok {
			return false
		}
	}
	return true
}
func (s *Service) Get(actor servicecatalog.Actor, id string) (ChangeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok || r.value.TenantID != actor.TenantID {
		return ChangeRequest{}, ErrNotFound
	}
	if !s.canRead(actor, r.value) {
		return ChangeRequest{}, ErrForbidden
	}
	return clone(r.value), nil
}
func (s *Service) List(actor servicecatalog.Actor) []ChangeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := []ChangeRequest{}
	for _, r := range s.records {
		if r.value.TenantID == actor.TenantID && s.canRead(actor, r.value) {
			values = append(values, clone(r.value))
		}
	}
	slices.SortFunc(values, func(a, b ChangeRequest) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return values
}
func (s *Service) Freeze(ctx context.Context, actor servicecatalog.Actor, id string, input SnapshotInput) (SnapshotResult, bool, error) {
	if !validKey(input.IdempotencyKey) || input.ExpectedVersion == 0 {
		return SnapshotResult{}, false, ErrValidation
	}
	current, err := s.Get(actor, id)
	if err != nil {
		return SnapshotResult{}, false, err
	}
	selections, entries, err := s.authorize(actor, current.Environment, "snapshot", input.Services)
	if err != nil {
		return SnapshotResult{}, false, err
	}
	// D1 keeps the service set fixed. A new snapshot can explicitly change branches.
	if len(selections) != len(current.Services) {
		return SnapshotResult{}, false, ErrValidation
	}
	for i, item := range selections {
		if item.ServiceID != current.Services[i].ServiceID {
			return SnapshotResult{}, false, ErrValidation
		}
	}
	input.Services = selections
	digest := hash(input)
	s.mu.Lock()
	r := s.records[id]
	if prior, ok := r.operations[input.IdempotencyKey]; ok {
		s.mu.Unlock()
		if prior.hash != digest {
			return SnapshotResult{}, false, ErrIdempotencyConflict
		}
		select {
		case <-ctx.Done():
			return SnapshotResult{}, false, ctx.Err()
		case <-prior.done:
			return cloneResult(prior.result), true, nil
		}
	}
	if r.value.Version != input.ExpectedVersion {
		s.mu.Unlock()
		return SnapshotResult{}, false, ErrVersionConflict
	}
	if r.busy {
		s.mu.Unlock()
		return SnapshotResult{}, false, ErrInProgress
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return SnapshotResult{}, false, err
	}
	op := &operation{hash: digest, done: make(chan struct{})}
	r.operations[input.IdempotencyKey] = op
	r.busy = true
	revision := uint64(len(r.value.Snapshots) + 1)
	s.mu.Unlock()
	snapshot := Snapshot{ID: fmt.Sprintf("snapshot-%d", revision), Revision: revision, State: "READY", CreatedBy: actor.Subject, CreatedAt: time.Now().UTC(), Services: []ServiceSnapshot{}}
	for i, item := range selections {
		entry := entries[i]
		resolved, err := s.resolve(ctx, repository.BranchSelection{RepositoryID: entry.Repository.RepositoryID, SourceBranch: item.SourceBranch, TargetBranch: item.TargetBranch})
		result := ServiceSnapshot{ServiceID: item.ServiceID, CatalogVersion: entry.CatalogVersion, ConnectorRef: entry.Repository.ConnectorRef, SourceBranch: item.SourceBranch, TargetBranch: item.TargetBranch, ResolvedAt: time.Now().UTC()}
		if err != nil {
			result.ErrorCode = resolutionError(err)
			snapshot.State = "FAILED"
		} else {
			result.Repository = &resolved
		}
		snapshot.Services = append(snapshot.Services, result)
	}
	// The hash includes tenant/environment, catalog bindings and immutable commits,
	// independently of observation timestamps.
	hashServices := cloneSnapshot(snapshot).Services
	for i := range hashServices {
		hashServices[i].ResolvedAt = time.Time{}
	}
	snapshot.InputHash = hash(struct {
		Tenant, Environment string
		Services            []ServiceSnapshot
	}{actor.TenantID, current.Environment, hashServices})
	s.mu.Lock()
	defer s.mu.Unlock()
	r.value.Version++
	r.value.Snapshots = append(r.value.Snapshots, snapshot)
	if snapshot.State == "READY" {
		r.value.CurrentSnapshotID = snapshot.ID
		r.value.Services = selections
	}
	r.busy = false
	op.result = SnapshotResult{ChangeID: id, ChangeVersion: r.value.Version, Snapshot: snapshot}
	close(op.done)
	return cloneResult(op.result), false, nil
}
func (s *Service) resolve(ctx context.Context, selection repository.BranchSelection) (task.RepositoryReference, error) {
	if err := ctx.Err(); err != nil {
		return task.RepositoryReference{}, err
	}
	if s.resolver == nil {
		return task.RepositoryReference{}, repository.ErrVerificationUnavailable
	}
	result, err := s.resolver.ResolveBranches(ctx, selection)
	if err != nil {
		return task.RepositoryReference{}, err
	}
	if result.Provider != "gitlab" || result.RepositoryID != selection.RepositoryID || result.TargetBranch != selection.TargetBranch || !repository.ValidObjectID(result.HeadSHA) || !repository.ValidObjectID(result.TargetSHA) || !repository.ValidObjectID(result.BaseSHA) {
		return task.RepositoryReference{}, repository.ErrVerificationUnavailable
	}
	return result, nil
}
func resolutionError(err error) string {
	switch {
	case errors.Is(err, repository.ErrSourceBranchNotFound):
		return "source_branch_not_found"
	case errors.Is(err, repository.ErrTargetBranchNotFound):
		return "target_branch_not_found"
	case errors.Is(err, repository.ErrRepositoryNotFound):
		return "repository_not_found"
	case errors.Is(err, repository.ErrReviewBaseNotFound):
		return "review_base_not_found"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "resolution_canceled"
	default:
		return "repository_resolution_unavailable"
	}
}
func cloneSnapshot(value Snapshot) Snapshot {
	value.Services = slices.Clone(value.Services)
	for i, item := range value.Services {
		if item.Repository != nil {
			reference := *item.Repository
			value.Services[i].Repository = &reference
		}
	}
	return value
}
func cloneResult(value SnapshotResult) SnapshotResult {
	value.Snapshot = cloneSnapshot(value.Snapshot)
	return value
}
func clone(value ChangeRequest) ChangeRequest {
	value.Services = slices.Clone(value.Services)
	value.Snapshots = slices.Clone(value.Snapshots)
	for i, item := range value.Snapshots {
		value.Snapshots[i] = cloneSnapshot(item)
	}
	return value
}
