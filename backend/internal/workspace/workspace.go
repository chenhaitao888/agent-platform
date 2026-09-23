package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

type State string

const (
	StateRegistered State = "REGISTERED"
	StatePreparing  State = "PREPARING"
	StateReady      State = "READY"
)

var (
	ErrTaskNotFound                   = errors.New("task not found")
	ErrTaskNotQueued                  = errors.New("task must be queued before workspace registration")
	ErrWorkspaceExists                = errors.New("workspace already exists for task")
	ErrIdempotencyConflict            = errors.New("idempotency key already used for another workspace registration")
	ErrWorkspaceNotFound              = errors.New("workspace not found")
	ErrVersionConflict                = errors.New("workspace version does not match expected version")
	ErrInvalidState                   = errors.New("workspace state does not allow preparation")
	ErrPreparationFailed              = errors.New("workspace preparation failed")
	ErrPreparationUnavailable         = errors.New("workspace preparation is unavailable")
	ErrPreparationIdempotencyConflict = errors.New("idempotency key already used for another workspace preparation")
)

type Preparer interface {
	Prepare(context.Context, task.RepositoryReference, string) error
}

type UnavailablePreparer struct{}

func (UnavailablePreparer) Prepare(context.Context, task.RepositoryReference, string) error {
	return ErrPreparationUnavailable
}

type Repository struct {
	Provider     string `json:"provider"`
	RepositoryID string `json:"repositoryId"`
}

type Workspace struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	TaskID       string     `json:"taskId"`
	Repository   Repository `json:"repository"`
	TargetBranch string     `json:"targetBranch"`
	TargetSHA    string     `json:"targetSha"`
	BaseSHA      string     `json:"baseSha"`
	HeadSHA      string     `json:"headSha"`
	Path         string     `json:"path,omitempty"`
	State        State      `json:"state"`
	Version      uint64     `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
}

type RegisterInput struct {
	RequestID      string
	TenantID       string
	TaskID         string
	IdempotencyKey string
}

type RegisterResult struct {
	Workspace Workspace
	Created   bool
}

type PrepareInput struct {
	RequestID       string
	TenantID        string
	TaskID          string
	IdempotencyKey  string
	ExpectedVersion uint64
}

type PrepareResult struct {
	Workspace Workspace
	Prepared  bool
}

type idempotencyScope struct {
	tenantID string
	key      string
}

type registrationRecord struct {
	taskID string
}

type preparationRecord struct {
	taskID          string
	expectedVersion uint64
	result          Workspace
}

type Manager struct {
	mu           sync.RWMutex
	tasks        *task.Store
	verifier     repository.ReferenceVerifier
	preparer     Preparer
	root         string
	nextID       uint64
	byTask       map[string]Workspace
	idempotency  map[idempotencyScope]registrationRecord
	preparations map[idempotencyScope]preparationRecord
}

func NewManager(tasks *task.Store, verifier repository.ReferenceVerifier) *Manager {
	return &Manager{
		tasks:        tasks,
		verifier:     verifier,
		preparer:     UnavailablePreparer{},
		byTask:       make(map[string]Workspace),
		idempotency:  make(map[idempotencyScope]registrationRecord),
		preparations: make(map[idempotencyScope]preparationRecord),
	}
}

func NewManagerWithPreparer(tasks *task.Store, verifier repository.ReferenceVerifier, preparer Preparer, root string) (*Manager, error) {
	if preparer == nil {
		return nil, errors.New("workspace preparer is required")
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("workspace root must be an absolute path")
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == string(filepath.Separator) {
		return nil, errors.New("workspace root must not be the filesystem root")
	}
	if err := os.MkdirAll(cleanRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if resolvedRoot == string(filepath.Separator) {
		return nil, errors.New("workspace root must not resolve to the filesystem root")
	}

	manager := NewManager(tasks, verifier)
	manager.preparer = preparer
	manager.root = resolvedRoot
	return manager, nil
}

func (m *Manager) Register(ctx context.Context, input RegisterInput) (RegisterResult, error) {
	currentTask, ok := m.tasks.Get(input.TaskID, input.TenantID)
	if !ok {
		return RegisterResult{}, ErrTaskNotFound
	}
	if currentTask.Status != task.StatusQueued {
		return RegisterResult{}, ErrTaskNotQueued
	}

	m.mu.Lock()
	if result, done, err := m.registrationResultLocked(input); done {
		m.mu.Unlock()
		return result, err
	}
	m.mu.Unlock()

	if err := m.verifier.Verify(ctx, currentTask.Repository); err != nil {
		return RegisterResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if result, done, err := m.registrationResultLocked(input); done {
		return result, err
	}

	m.nextID++
	registered := Workspace{
		ID:       fmt.Sprintf("workspace-%d", m.nextID),
		TenantID: input.TenantID,
		TaskID:   input.TaskID,
		Repository: Repository{
			Provider:     currentTask.Repository.Provider,
			RepositoryID: currentTask.Repository.RepositoryID,
		},
		TargetBranch: currentTask.Repository.TargetBranch,
		TargetSHA:    currentTask.Repository.TargetSHA,
		BaseSHA:      currentTask.Repository.BaseSHA,
		HeadSHA:      currentTask.Repository.HeadSHA,
		State:        StateRegistered,
		Version:      1,
		CreatedAt:    time.Now().UTC(),
	}
	m.byTask[input.TaskID] = registered
	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	m.idempotency[scope] = registrationRecord{
		taskID: input.TaskID,
	}
	m.appendWorkspaceEvent(registered, task.EventTypeWorkspaceRegistered, input.RequestID, registered.CreatedAt)
	return RegisterResult{Workspace: registered, Created: true}, nil
}

func (m *Manager) appendWorkspaceEvent(current Workspace, eventType task.EventType, requestID string, occurredAt time.Time) {
	// 调用方已在 Manager 锁内完成状态变化；Task Store 再用自己的锁分配该 Task 的事件序号。
	// 这保持单进程的追加顺序，但不是跨 Store 的数据库事务。
	m.tasks.AppendEvent(task.AppendEventInput{
		TenantID:    current.TenantID,
		TaskID:      current.TaskID,
		EventType:   eventType,
		CausationID: requestID,
		OccurredAt:  occurredAt,
		Payload: task.EventPayload{Workspace: &task.WorkspaceEventPayload{
			WorkspaceID: current.ID,
			State:       string(current.State),
			Version:     current.Version,
		}},
	})
}

func (m *Manager) registrationResultLocked(input RegisterInput) (RegisterResult, bool, error) {
	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if record, ok := m.idempotency[scope]; ok {
		if record.taskID != input.TaskID {
			return RegisterResult{}, true, ErrIdempotencyConflict
		}
		return RegisterResult{Workspace: m.byTask[record.taskID]}, true, nil
	}
	if _, exists := m.byTask[input.TaskID]; exists {
		return RegisterResult{}, true, ErrWorkspaceExists
	}
	return RegisterResult{}, false, nil
}

func (m *Manager) Get(taskID, tenantID string) (Workspace, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	found, ok := m.byTask[taskID]
	if !ok || found.TenantID != tenantID {
		return Workspace{}, false
	}
	return found, true
}

func (m *Manager) Prepare(ctx context.Context, input PrepareInput) (PrepareResult, error) {
	m.mu.Lock()
	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if record, ok := m.preparations[scope]; ok {
		if record.taskID != input.TaskID || record.expectedVersion != input.ExpectedVersion {
			m.mu.Unlock()
			return PrepareResult{}, ErrPreparationIdempotencyConflict
		}
		m.mu.Unlock()
		return PrepareResult{Workspace: record.result}, nil
	}
	current, ok := m.byTask[input.TaskID]
	if !ok || current.TenantID != input.TenantID {
		m.mu.Unlock()
		return PrepareResult{}, ErrWorkspaceNotFound
	}
	if current.Version != input.ExpectedVersion {
		m.mu.Unlock()
		return PrepareResult{}, ErrVersionConflict
	}
	if current.State != StateRegistered {
		m.mu.Unlock()
		return PrepareResult{}, ErrInvalidState
	}
	if m.root == "" {
		m.mu.Unlock()
		return PrepareResult{}, ErrPreparationUnavailable
	}

	current.State = StatePreparing
	current.Version++
	m.byTask[input.TaskID] = current
	m.appendWorkspaceEvent(current, task.EventTypeWorkspacePreparing, input.RequestID, time.Now().UTC())
	m.mu.Unlock()

	destination := filepath.Join(m.root, current.ID, "worktree")
	reference := task.RepositoryReference{
		Provider:     current.Repository.Provider,
		RepositoryID: current.Repository.RepositoryID,
		BaseSHA:      current.BaseSHA,
		HeadSHA:      current.HeadSHA,
	}
	if err := m.preparer.Prepare(ctx, reference, destination); err != nil {
		m.mu.Lock()
		// Go map 缺键会返回零值；必须同时检查 ok，不能把空 Workspace 写回去。
		// 类比 Java：Map.get 得到 null 后，不能继续修改并 put 回同一个 key。
		current, ok = m.byTask[input.TaskID]
		if !ok {
			m.mu.Unlock()
			return PrepareResult{}, ErrWorkspaceNotFound
		}
		current.State = StateRegistered
		current.Version++
		m.byTask[input.TaskID] = current
		// 事件只记录外部可见的最终状态，不把 Git 的内部诊断或凭据写入时间线。
		m.appendWorkspaceEvent(current, task.EventTypeWorkspacePreparationFailed, input.RequestID, time.Now().UTC())
		m.mu.Unlock()
		if errors.Is(err, ErrPreparationUnavailable) {
			return PrepareResult{}, err
		}
		return PrepareResult{}, fmt.Errorf("%w: %v", ErrPreparationFailed, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok = m.byTask[input.TaskID]
	if !ok {
		return PrepareResult{}, ErrWorkspaceNotFound
	}
	current.State = StateReady
	current.Path = destination
	current.Version++
	m.byTask[input.TaskID] = current
	m.preparations[scope] = preparationRecord{
		taskID:          input.TaskID,
		expectedVersion: input.ExpectedVersion,
		result:          current,
	}
	m.appendWorkspaceEvent(current, task.EventTypeWorkspaceReady, input.RequestID, time.Now().UTC())
	return PrepareResult{Workspace: current, Prepared: true}, nil
}
