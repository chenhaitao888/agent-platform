package task

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type Status string

// Phase 0 的目标分支政策；实际参与计算的是创建 Task 时解析出的 TargetSHA。
const ReviewTargetBranch = "master"

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

// RepositoryReference 保存一次任务的不可变代码坐标：TargetSHA 是 master 快照，
// BaseSHA 是它与 HeadSHA 的 merge-base；评审 diff 使用 BaseSHA，不直接使用 TargetSHA。
type RepositoryReference struct {
	Provider     string `json:"provider"`
	RepositoryID string `json:"repositoryId"`
	TargetBranch string `json:"targetBranch"`
	TargetSHA    string `json:"targetSha"`
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
	// v2 将原来扁平的 Task status/version 移入 payload.task，并为其他事实留出独立分支。
	EventSchemaVersion = "2.0"

	EventTypeTaskCreated                EventType = "task.created"
	EventTypeTaskQueued                 EventType = "task.queued"
	EventTypeWorkspaceRegistered        EventType = "workspace.registered"
	EventTypeWorkspacePreparing         EventType = "workspace.preparing"
	EventTypeWorkspaceReady             EventType = "workspace.ready"
	EventTypeWorkspacePreparationFailed EventType = "workspace.preparation_failed"
	EventTypeArtifactCreated            EventType = "artifact.created"
)

// EventType 决定下面哪个分支有值；三种 version 分属不同对象，不能混用。
// Java 可类比由 Task/Workspace/Artifact record 组成的 sealed payload 类型。
type EventPayload struct {
	Task      *TaskEventPayload      `json:"task,omitempty"`
	Workspace *WorkspaceEventPayload `json:"workspace,omitempty"`
	Artifact  *ArtifactEventPayload  `json:"artifact,omitempty"`
}

type TaskEventPayload struct {
	Status  Status `json:"status"`
	Version uint64 `json:"version"`
}

type WorkspaceEventPayload struct {
	WorkspaceID string `json:"workspaceId"`
	State       string `json:"state"`
	Version     uint64 `json:"version"`
}

type ArtifactEventPayload struct {
	ArtifactID  string `json:"artifactId"`
	WorkspaceID string `json:"workspaceId"`
	Type        string `json:"type"`
	MediaType   string `json:"mediaType"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type AppendEventInput struct {
	TenantID    string
	TaskID      string
	EventType   EventType
	CausationID string
	OccurredAt  time.Time
	Payload     EventPayload
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

// ReplayCreate 在访问会变化的 master 之前检查幂等键：同一请求应返回第一次固定的快照。
// Java 可类比先查幂等操作表，再调用外部 GitLab；不能每次重试都重新解析分支。
func (s *Store) ReplayCreate(input CreateInput) (CreateResult, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskID, ok := s.idempotency[idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}]
	if !ok {
		return CreateResult{}, false, nil
	}
	existing := s.tasks[taskID]
	if !sameCreateRequest(existing, input) {
		return CreateResult{}, true, ErrIdempotencyConflict
	}
	return CreateResult{Task: existing}, true, nil
}

func sameCreateRequest(existing Task, input CreateInput) bool {
	return existing.Type == input.Type &&
		existing.Goal == input.Goal &&
		existing.Repository.Provider == input.Repository.Provider &&
		existing.Repository.RepositoryID == input.Repository.RepositoryID &&
		existing.Repository.HeadSHA == input.Repository.HeadSHA
}

func (s *Store) Create(input CreateInput) (CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if taskID, ok := s.idempotency[scope]; ok {
		existing := s.tasks[taskID]
		if !sameCreateRequest(existing, input) {
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
	s.appendEventLocked(AppendEventInput{
		TenantID:    created.TenantID,
		TaskID:      created.ID,
		EventType:   EventTypeTaskCreated,
		CausationID: input.RequestID,
		OccurredAt:  now,
		Payload: EventPayload{Task: &TaskEventPayload{
			Status:  created.Status,
			Version: created.Version,
		}},
	})

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
	s.appendEventLocked(AppendEventInput{
		TenantID:    current.TenantID,
		TaskID:      current.ID,
		EventType:   EventTypeTaskQueued,
		CausationID: input.RequestID,
		OccurredAt:  time.Now().UTC(),
		Payload: EventPayload{Task: &TaskEventPayload{
			Status:  current.Status,
			Version: current.Version,
		}},
	})
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
	for index, event := range stored {
		listed[index] = event
		listed[index].Payload = event.Payload.clone()
	}
	return listed, true
}

// AppendEvent 供同一进程中的 Workspace/Artifact 操作追加 Task 范围内的已发生事实。
// 调用方只在首次状态变化后调用；幂等重放不得重复调用。
func (s *Store) AppendEvent(input AppendEventInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(input)
}

func (s *Store) appendEventLocked(input AppendEventInput) {
	s.nextEventID++
	sequence := uint64(len(s.events[input.TaskID]) + 1)
	event := Event{
		SchemaVersion: EventSchemaVersion,
		EventID:       fmt.Sprintf("evt-%d", s.nextEventID),
		EventType:     input.EventType,
		OccurredAt:    input.OccurredAt,
		TenantID:      input.TenantID,
		TaskID:        input.TaskID,
		Sequence:      sequence,
		CorrelationID: input.TaskID,
		CausationID:   input.CausationID,
		Payload:       input.Payload.clone(),
	}
	s.events[input.TaskID] = append(s.events[input.TaskID], event)
}

func (p EventPayload) clone() EventPayload {
	cloned := p
	if p.Task != nil {
		taskPayload := *p.Task
		cloned.Task = &taskPayload
	}
	if p.Workspace != nil {
		workspacePayload := *p.Workspace
		cloned.Workspace = &workspacePayload
	}
	if p.Artifact != nil {
		artifactPayload := *p.Artifact
		cloned.Artifact = &artifactPayload
	}
	return cloned
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
