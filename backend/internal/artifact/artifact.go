package artifact

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Type string

const TypeRepositoryDiff Type = "REPOSITORY_DIFF"

var ErrIdempotencyConflict = errors.New("idempotency key already used with different Artifact content")

type Artifact struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	TaskID      string    `json:"taskId"`
	WorkspaceID string    `json:"workspaceId"`
	Type        Type      `json:"type"`
	MediaType   string    `json:"mediaType"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

type CreateInput struct {
	TenantID       string
	TaskID         string
	WorkspaceID    string
	IdempotencyKey string
	Type           Type
	MediaType      string
	Content        []byte
}

type CreateResult struct {
	Artifact Artifact
	Created  bool
}

type storedArtifact struct {
	metadata Artifact
	content  []byte
}

type idempotencyScope struct {
	tenantID string
	key      string
}

type creationRecord struct {
	artifactID string
}

type Store struct {
	mu      sync.RWMutex
	nextID  uint64
	byID    map[string]storedArtifact
	creates map[idempotencyScope]creationRecord
}

func NewStore() *Store {
	return &Store{
		byID:    make(map[string]storedArtifact),
		creates: make(map[idempotencyScope]creationRecord),
	}
}

func (s *Store) Create(input CreateInput) (CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	scope := idempotencyScope{tenantID: input.TenantID, key: input.IdempotencyKey}
	if record, ok := s.creates[scope]; ok {
		stored := s.byID[record.artifactID]
		if stored.metadata.TaskID != input.TaskID ||
			stored.metadata.WorkspaceID != input.WorkspaceID ||
			stored.metadata.Type != input.Type ||
			stored.metadata.MediaType != input.MediaType ||
			!bytes.Equal(stored.content, input.Content) {
			return CreateResult{}, ErrIdempotencyConflict
		}
		return CreateResult{Artifact: stored.metadata}, nil
	}

	digest := sha256.Sum256(input.Content)
	s.nextID++
	created := Artifact{
		ID:          fmt.Sprintf("artifact-%d", s.nextID),
		TenantID:    input.TenantID,
		TaskID:      input.TaskID,
		WorkspaceID: input.WorkspaceID,
		Type:        input.Type,
		MediaType:   input.MediaType,
		SHA256:      fmt.Sprintf("%x", digest),
		SizeBytes:   int64(len(input.Content)),
		CreatedAt:   time.Now().UTC(),
	}
	// []byte 是 slice，会共享底层数组。写入时复制，避免调用方之后修改原 slice，
	// 把已经归档的内容悄悄改掉。
	contentCopy := append([]byte(nil), input.Content...)
	s.byID[created.ID] = storedArtifact{metadata: created, content: contentCopy}
	s.creates[scope] = creationRecord{artifactID: created.ID}
	return CreateResult{Artifact: created, Created: true}, nil
}

func (s *Store) GetContent(id, tenantID string) (Artifact, []byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, ok := s.byID[id]
	if !ok || stored.metadata.TenantID != tenantID {
		return Artifact{}, nil, false
	}
	// 读取时也返回副本；否则调用方拿到 slice 后仍能修改 Store 内部数组。
	contentCopy := append([]byte(nil), stored.content...)
	return stored.metadata, contentCopy, true
}

func (s *Store) Get(id, tenantID string) (Artifact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, ok := s.byID[id]
	if !ok || stored.metadata.TenantID != tenantID {
		return Artifact{}, false
	}
	return stored.metadata, true
}
