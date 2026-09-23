package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestWorkspaceRegistrationAppendsATaskEvent(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)

	items := taskEventItemsForTest(t, handler)
	if len(items) != 3 {
		t.Fatalf("expected task creation, queue and workspace registration events, got %d", len(items))
	}
	var registered struct {
		SchemaVersion string `json:"schemaVersion"`
		EventType     string `json:"eventType"`
		Sequence      uint64 `json:"sequence"`
		CausationID   string `json:"causationId"`
		Payload       struct {
			Workspace struct {
				WorkspaceID string `json:"workspaceId"`
				State       string `json:"state"`
				Version     uint64 `json:"version"`
			} `json:"workspace"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(items[2], &registered); err != nil {
		t.Fatalf("decode workspace event: %v", err)
	}
	if registered.SchemaVersion != "2.0" || registered.EventType != "workspace.registered" || registered.Sequence != 3 || registered.CausationID != "req-register-workspace-for-get" {
		t.Fatalf("unexpected workspace event envelope: %#v", registered)
	}
	if registered.Payload.Workspace.WorkspaceID != "workspace-1" || registered.Payload.Workspace.State != "REGISTERED" || registered.Payload.Workspace.Version != 1 {
		t.Fatalf("unexpected workspace event payload: %#v", registered.Payload.Workspace)
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace", strings.NewReader(`{
		"requestId":"req-register-replay",
		"idempotencyKey":"register-workspace-for-get",
		"tenantId":"tenant-a"
	}`)))
	if replay.Code != http.StatusOK || len(taskEventItemsForTest(t, handler)) != 3 {
		t.Fatalf("registration replay must not append an event: status %d", replay.Code)
	}
}

func TestWorkspacePreparationAppendsPreparingAndReadyEvents(t *testing.T) {
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-events",
		"idempotencyKey":"prepare-events",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("prepare Workspace: expected 200, got %d: %s", response.Code, response.Body.String())
	}

	items := taskEventItemsForTest(t, handler)
	if len(items) != 5 {
		t.Fatalf("expected five events through READY, got %d", len(items))
	}
	for index, want := range []struct {
		eventType string
		state     string
		version   uint64
	}{
		{"workspace.preparing", "PREPARING", 2},
		{"workspace.ready", "READY", 3},
	} {
		var event struct {
			EventType   string `json:"eventType"`
			Sequence    uint64 `json:"sequence"`
			CausationID string `json:"causationId"`
			Payload     struct {
				Workspace struct {
					WorkspaceID string `json:"workspaceId"`
					State       string `json:"state"`
					Version     uint64 `json:"version"`
				} `json:"workspace"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(items[index+3], &event); err != nil {
			t.Fatalf("decode preparation event: %v", err)
		}
		if event.EventType != want.eventType || event.Sequence != uint64(index+4) || event.CausationID != "req-prepare-events" {
			t.Fatalf("unexpected preparation event envelope: %#v", event)
		}
		if event.Payload.Workspace.WorkspaceID != "workspace-1" || event.Payload.Workspace.State != want.state || event.Payload.Workspace.Version != want.version {
			t.Fatalf("unexpected preparation event payload: %#v", event.Payload.Workspace)
		}
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-replay",
		"idempotencyKey":"prepare-events",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`)))
	if replay.Code != http.StatusOK || len(taskEventItemsForTest(t, handler)) != 5 {
		t.Fatalf("preparation replay must not append events: status %d", replay.Code)
	}
}

func TestWorkspacePreparationFailureAppendsARedactedFact(t *testing.T) {
	handler, err := NewHandlerWithWorkspacePreparer(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error {
			return errors.New("private Git diagnostic")
		}),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-failed-event",
		"idempotencyKey":"prepare-failed-event",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("prepare Workspace: expected 503, got %d: %s", response.Code, response.Body.String())
	}
	items := taskEventItemsForTest(t, handler)
	if len(items) != 5 {
		t.Fatalf("expected five events through preparation failure, got %d", len(items))
	}
	var failed struct {
		EventType   string `json:"eventType"`
		Sequence    uint64 `json:"sequence"`
		CausationID string `json:"causationId"`
		Payload     struct {
			Workspace struct {
				State   string `json:"state"`
				Version uint64 `json:"version"`
			} `json:"workspace"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(items[4], &failed); err != nil {
		t.Fatalf("decode failure event: %v", err)
	}
	if failed.EventType != "workspace.preparation_failed" || failed.Sequence != 5 || failed.CausationID != "req-prepare-failed-event" {
		t.Fatalf("unexpected failure event envelope: %#v", failed)
	}
	if failed.Payload.Workspace.State != "REGISTERED" || failed.Payload.Workspace.Version != 3 {
		t.Fatalf("expected restored REGISTERED version 3, got %#v", failed.Payload.Workspace)
	}
	if strings.Contains(string(items[4]), "private Git diagnostic") {
		t.Fatal("internal Git diagnostic must not appear in the domain event")
	}
}

func TestArchiveDiffAppendsAnArtifactCreatedEvent(t *testing.T) {
	patch := []byte("diff --git a/README.md b/README.md\n+head\n")
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) { return patch, nil }),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	createQueuedTaskForWorkspaceTest(t, handler)
	registerWorkspaceForTest(t, handler)
	prepareResponse := httptest.NewRecorder()
	handler.ServeHTTP(prepareResponse, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace/prepare", strings.NewReader(`{
		"requestId":"req-prepare-before-archive",
		"idempotencyKey":"prepare-before-archive",
		"tenantId":"tenant-a",
		"expectedVersion":1
	}`)))
	if prepareResponse.Code != http.StatusOK {
		t.Fatalf("prepare Workspace: expected 200, got %d: %s", prepareResponse.Code, prepareResponse.Body.String())
	}
	archiveResponse := httptest.NewRecorder()
	handler.ServeHTTP(archiveResponse, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/artifacts/diff", strings.NewReader(`{
		"requestId":"req-archive-event",
		"idempotencyKey":"archive-event",
		"tenantId":"tenant-a",
		"expectedWorkspaceVersion":3
	}`)))
	if archiveResponse.Code != http.StatusCreated {
		t.Fatalf("archive diff: expected 201, got %d: %s", archiveResponse.Code, archiveResponse.Body.String())
	}
	items := taskEventItemsForTest(t, handler)
	if len(items) != 6 {
		t.Fatalf("expected six events through Artifact creation, got %d", len(items))
	}
	var created struct {
		EventType   string `json:"eventType"`
		Sequence    uint64 `json:"sequence"`
		CausationID string `json:"causationId"`
		Payload     struct {
			Artifact struct {
				ArtifactID  string `json:"artifactId"`
				WorkspaceID string `json:"workspaceId"`
				Type        string `json:"type"`
				SHA256      string `json:"sha256"`
				SizeBytes   int64  `json:"sizeBytes"`
			} `json:"artifact"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(items[5], &created); err != nil {
		t.Fatalf("decode Artifact event: %v", err)
	}
	if created.EventType != "artifact.created" || created.Sequence != 6 || created.CausationID != "req-archive-event" {
		t.Fatalf("unexpected Artifact event envelope: %#v", created)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256(patch))
	if created.Payload.Artifact.ArtifactID != "artifact-1" || created.Payload.Artifact.WorkspaceID != "workspace-1" || created.Payload.Artifact.Type != "REPOSITORY_DIFF" || created.Payload.Artifact.SHA256 != wantSHA || created.Payload.Artifact.SizeBytes != int64(len(patch)) {
		t.Fatalf("unexpected Artifact event payload: %#v", created.Payload.Artifact)
	}
	if strings.Contains(string(items[5]), string(patch)) {
		t.Fatal("Artifact event must contain metadata, not diff content")
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/artifacts/diff", strings.NewReader(`{
		"requestId":"req-archive-replay",
		"idempotencyKey":"archive-event",
		"tenantId":"tenant-a",
		"expectedWorkspaceVersion":3
	}`)))
	if replay.Code != http.StatusOK || len(taskEventItemsForTest(t, handler)) != 6 {
		t.Fatalf("Artifact archive replay must not append an event: status %d", replay.Code)
	}
}

func taskEventItemsForTest(t *testing.T, handler http.Handler) []json.RawMessage {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/events?tenantId=tenant-a", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list Task events: expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Task events: %v", err)
	}
	return body.Items
}
