package task

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type Status string

const (
	StatusCreated Status = "CREATED"
	StatusQueued  Status = "QUEUED"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key already used with different task content")
	ErrTaskNotFound        = errors.New("task not found")
	ErrVersionConflict     = errors.New("task version does not match expected version")
	ErrInvalidTransition   = errors.New("task status transition is not allowed")
)

type RepositoryReference struct {
	Provider     string `json:"provider"`
	RepositoryID string `json:"repositoryId"`
	BaseSHA      string `json:"baseSha"`
	HeadSHA      string `json:"headSha"`
}

type Task struct {
	ID             string              `json:"id"`
	TenantID       string              `json:"tenantId"`
	IdempotencyKey string              `json:"idempotencyKey"`
	Type           string              `json:"type"`
	Goal           string              `json:"goal"`
	Repository     RepositoryReference `json:"repository"`
	Status         Status              `json:"status"`
	Version        uint64              `json:"version"`
	CreatedAt      time.Time           `json:"createdAt"`
}

type EventType string

const (
	EventTypeTaskCreated EventType = "task.created"
	EventTypeTaskQueued  EventType = "task.queued"
)

type EventPayload struct {
	Status  Status `json:"status"`
	Version uint64 `json:"version"`
}

type Event struct {
	SchemaVersion string       `json:"schemaVersion"`
	EventID       string       `json:"eventId"`
	EventType     EventType    `json:"eventType"`
	OccurredAt    time.Time    `json:"occurredAt"`
	TenantID      string       `json:"tenantId"`
	TaskID        string       `json:"taskId"`
	Sequence      uint64       `json:"sequence"`
	CorrelationID string       `json:"correlationId"`
	CausationID   string       `json:"causationId"`
	Payload       EventPayload `json:"payload"`
}

type CreateInput struct {
	RequestID      string
	TenantID       string
	IdempotencyKey string
	Type           string
	Goal           string
	Repository     RepositoryReference
}

type CreateResult struct {
	Task    Task
	Created bool
}

type TransitionInput struct {
	TaskID          string
	RequestID       string
	TenantID        string
	IdempotencyKey  string
	ExpectedVersion uint64
	Status          Status
}

type idempotencyScope struct {
	tenantID string
	key      string
}

type transitionRecord struct {
	taskID          string
	expectedVersion uint64
	status          Status
	result          Task
}

type Store struct {
	mu          sync.RWMutex
	nextID      uint64
	nextEventID uint64
	tasks       map[string]Task
	order       []string
	idempotency map[idempotencyScope]string
	transitions map[idempotencyScope]transitionRecord
	events      map[string][]Event
}

func NewStore() *Store {
	return &Store{
		tasks:       make(map[string]Task),
		idempotency: make(map[idempotencyScope]string),
		transitions: make(map[idempotencyScope]transitionRecord),
		events:      make(map[string][]Event),
	}
}

func (s *Store) Create(input CreateInput) (CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if taskID, ok := s.idempotency[scope]; ok {
		existing := s.tasks[taskID]
		if existing.Type != input.Type || existing.Goal != input.Goal || existing.Repository != input.Repository {
			return CreateResult{}, ErrIdempotencyConflict
		}
		return CreateResult{Task: existing}, nil
	}

	s.nextID++
	now := time.Now().UTC()
	created := Task{
		ID:             fmt.Sprintf("task-%d", s.nextID),
		TenantID:       input.TenantID,
		IdempotencyKey: input.IdempotencyKey,
		Type:           input.Type,
		Goal:           input.Goal,
		Repository:     input.Repository,
		Status:         StatusCreated,
		Version:        1,
		CreatedAt:      now,
	}
	s.tasks[created.ID] = created
	s.order = append(s.order, created.ID)
	s.idempotency[scope] = created.ID
	s.appendEventLocked(created, EventTypeTaskCreated, input.RequestID, now)

	return CreateResult{Task: created, Created: true}, nil
}

// Get 在数据读取边界同时检查租户；调用方不能先按 ID 读出其他租户的 Task 再自行过滤。
func (s *Store) Get(id, tenantID string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	found, ok := s.tasks[id]
	if !ok || found.TenantID != tenantID {
		return Task{}, false
	}
	return found, true
}

func (s *Store) Transition(input TransitionInput) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if record, ok := s.transitions[scope]; ok {
		if record.taskID != input.TaskID || record.expectedVersion != input.ExpectedVersion || record.status != input.Status {
			return Task{}, ErrIdempotencyConflict
		}
		return record.result, nil
	}

	current, ok := s.tasks[input.TaskID]
	if !ok || current.TenantID != input.TenantID {
		return Task{}, ErrTaskNotFound
	}
	if current.Version != input.ExpectedVersion {
		return Task{}, ErrVersionConflict
	}
	if current.Status != StatusCreated || input.Status != StatusQueued {
		return Task{}, ErrInvalidTransition
	}

	current.Status = input.Status
	current.Version++
	s.tasks[current.ID] = current
	s.transitions[scope] = transitionRecord{
		taskID:          input.TaskID,
		expectedVersion: input.ExpectedVersion,
		status:          input.Status,
		result:          current,
	}
	s.appendEventLocked(current, EventTypeTaskQueued, input.RequestID, time.Now().UTC())
	return current, nil
}

func (s *Store) ListEvents(taskID, tenantID string) ([]Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	current, ok := s.tasks[taskID]
	if !ok || current.TenantID != tenantID {
		return nil, false
	}

	stored := s.events[taskID]
	listed := make([]Event, len(stored))
	copy(listed, stored)
	return listed, true
}

func (s *Store) appendEventLocked(current Task, eventType EventType, causationID string, occurredAt time.Time) {
	s.nextEventID++
	sequence := uint64(len(s.events[current.ID]) + 1)
	event := Event{
		SchemaVersion: "1.0",
		EventID:       fmt.Sprintf("evt-%d", s.nextEventID),
		EventType:     eventType,
		OccurredAt:    occurredAt,
		TenantID:      current.TenantID,
		TaskID:        current.ID,
		Sequence:      sequence,
		CorrelationID: current.ID,
		CausationID:   causationID,
		Payload: EventPayload{
			Status:  current.Status,
			Version: current.Version,
		},
	}
	s.events[current.ID] = append(s.events[current.ID], event)
}

// List 保持最新创建在前，同时只复制当前租户的 Task；空 slice 会编码为 JSON []。
func (s *Store) List(tenantID string) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	listed := make([]Task, 0, len(s.order))
	for index := len(s.order) - 1; index >= 0; index-- {
		current := s.tasks[s.order[index]]
		if current.TenantID == tenantID {
			listed = append(listed, current)
		}
	}
	return listed
}
