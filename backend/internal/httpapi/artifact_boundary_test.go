package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

const artifactBoundaryPatch = "diff --git a/review.md b/review.md\n-old\n+complete\n"

func TestArchiveDiffRejectsWorkspaceVersionsBeforeReading(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		version uint64
		status  int
		body    errorResponse
	}{
		{"zero version", 0, http.StatusBadRequest, errorResponse{"validation_error", "requestId, idempotencyKey, tenantId and expectedWorkspaceVersion are required"}},
		{"stale version", 2, http.StatusConflict, errorResponse{"version_conflict", "workspace version does not match expectedWorkspaceVersion"}},
		{"future version", 4, http.StatusConflict, errorResponse{"version_conflict", "workspace version does not match expectedWorkspaceVersion"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var reads atomic.Uint64
			handler := newHandlerForArtifactBoundaryTest(t, diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
				reads.Add(1)
				return []byte(artifactBoundaryPatch), nil
			}))
			ready := readyWorkspaceForArtifactTest(t, handler, "tenant-a", "version-boundary")
			before := preparationEventsForTest(t, handler, ready)
			input := ready
			input.Version = scenario.version
			const key = "archive-version-boundary"
			const requestID = "req-archive-invalid-version"
			response := postDiffArchiveForTest(handler, context.Background(), input, key, requestID)
			responseRequestID := requestID
			if scenario.version == 0 {
				// 字段校验尚未通过，不要求采用 body 中的 requestId。
				responseRequestID = ""
			}
			assertArchiveErrorForTest(t, response, scenario.status, scenario.body, responseRequestID)
			if reads.Load() != 0 {
				t.Fatal("invalid versions must be rejected before diff reading")
			}
			assertArtifactMissingForTest(t, handler, ready.TenantID)
			if !reflect.DeepEqual(preparationEventsForTest(t, handler, ready), before) || getPreparationWorkspaceForTest(t, handler, ready) != ready {
				t.Fatal("version rejection must preserve the Workspace and timeline")
			}
			assertArtifactRetryForTest(t, handler, ready, key, "req-archive-correct-version")
		})
	}
}

func TestArchiveDiffReadFailureLeavesNoArtifactAndAllowsRetry(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		failure error
		status  int
		body    errorResponse
	}{
		{"oversized diff", fmt.Errorf("%w: private Git diagnostic", repository.ErrDiffTooLarge), http.StatusRequestEntityTooLarge, errorResponse{"diff_too_large", "repository diff exceeds the supported size"}},
		{"unavailable diff", fmt.Errorf("%w: private Git diagnostic", repository.ErrDiffUnavailable), http.StatusServiceUnavailable, errorResponse{"diff_unavailable", "repository diff is unavailable"}},
		{"unexpected failure", errors.New("private Git diagnostic"), http.StatusInternalServerError, errorResponse{"internal_error", "repository diff could not be archived"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var reads atomic.Uint64
			handler := newHandlerForArtifactBoundaryTest(t, diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
				if reads.Add(1) == 1 {
					// I/O 可能同时交付部分字节和错误，错误路径不能把这些字节归档。
					return []byte("partial diff bytes must stay internal"), scenario.failure
				}
				return []byte(artifactBoundaryPatch), nil
			}))
			ready := readyWorkspaceForArtifactTest(t, handler, "tenant-a", "read-failure")
			before := preparationEventsForTest(t, handler, ready)
			const key = "archive-read-failure"
			const requestID = "req-archive-read-failure"
			response := postDiffArchiveForTest(handler, context.Background(), ready, key, requestID)
			assertArchiveErrorForTest(t, response, scenario.status, scenario.body, requestID)
			if reads.Load() != 1 {
				t.Fatalf("expected one failed diff read, got %d", reads.Load())
			}
			assertArtifactMissingForTest(t, handler, ready.TenantID)
			if !reflect.DeepEqual(preparationEventsForTest(t, handler, ready), before) || getPreparationWorkspaceForTest(t, handler, ready) != ready {
				t.Fatal("read failure must preserve the Workspace and timeline")
			}
			assertArtifactRetryForTest(t, handler, ready, key, "req-archive-read-retry")
		})
	}
}

func TestArchiveDiffChecksWorkspaceOwnershipAndReadinessBeforeReading(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   errorResponse
	}{
		{"missing Workspace", http.StatusNotFound, errorResponse{"not_found", "workspace not found"}},
		{"REGISTERED Workspace", http.StatusConflict, errorResponse{"workspace_not_ready", "workspace must be READY before archiving its diff"}},
		{"another tenant", http.StatusNotFound, errorResponse{"not_found", "workspace not found"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var reads atomic.Uint64
			handler := newHandlerForArtifactBoundaryTest(t, diffReaderFunc(func(context.Context, repository.DiffInput) ([]byte, error) {
				reads.Add(1)
				return []byte(artifactBoundaryPatch), nil
			}))
			var owner workspace.Workspace
			switch scenario.name {
			case "missing Workspace":
				queued := createQueuedTaskForTest(t, handler, "tenant-a", "missing-workspace")
				owner = workspace.Workspace{TaskID: queued.ID, TenantID: queued.TenantID, Version: 1}
			case "REGISTERED Workspace":
				owner = registerPreparationWorkspaceForTest(t, handler, "tenant-a", "registered-workspace")
			case "another tenant":
				owner = readyWorkspaceForArtifactTest(t, handler, "tenant-a", "owned-workspace")
			}
			input := owner
			if scenario.name == "another tenant" {
				input.TenantID = "tenant-b"
			}
			before := preparationEventsForTest(t, handler, owner)
			const key = "archive-precondition"
			const requestID = "req-archive-precondition"
			response := postDiffArchiveForTest(handler, context.Background(), input, key, requestID)
			assertArchiveErrorForTest(t, response, scenario.status, scenario.body, requestID)
			if reads.Load() != 0 {
				t.Fatal("missing, unready or unowned Workspace must not trigger diff reading")
			}
			assertArtifactMissingForTest(t, handler, owner.TenantID)
			if !reflect.DeepEqual(preparationEventsForTest(t, handler, owner), before) {
				t.Fatal("rejected archive must preserve the owner's timeline")
			}
			if scenario.name == "missing Workspace" {
				get := httptest.NewRecorder()
				handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+owner.TaskID+"/workspace?tenantId="+owner.TenantID, nil))
				if get.Code != http.StatusNotFound {
					t.Fatalf("archive must not register a missing Workspace: %d: %s", get.Code, get.Body.String())
				}
				registerWorkspaceForTest(t, handler)
			} else if getPreparationWorkspaceForTest(t, handler, owner) != owner {
				t.Fatal("rejected archive must preserve the owner's Workspace")
			}
			var ready workspace.Workspace
			if scenario.name == "another tenant" {
				// 该租户改用自己的 READY Workspace，先前被拒绝的 key 仍可使用。
				ready = readyWorkspaceForArtifactTest(t, handler, input.TenantID, "second-owner")
			} else {
				prepareWorkspaceForTest(t, handler, "prepare-after-archive-rejection")
				ready = getPreparationWorkspaceForTest(t, handler, owner)
			}
			assertArtifactRetryForTest(t, handler, ready, key, "req-archive-ready-owner")
			if scenario.name == "another tenant" && (!reflect.DeepEqual(preparationEventsForTest(t, handler, owner), before) || getPreparationWorkspaceForTest(t, handler, owner) != owner) {
				t.Fatal("another tenant's successful archive must leave the original Task untouched")
			}
		})
	}
}

func newHandlerForArtifactBoundaryTest(t *testing.T, reader repository.DiffReader) http.Handler {
	t.Helper()
	handler, err := NewHandlerWithWorkspaceServices(
		repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
		workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
		reader,
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	return handler
}

func assertArchiveErrorForTest(t *testing.T, response *httptest.ResponseRecorder, status int, expected errorResponse, requestID string) {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode archive error: %v", err)
	}
	if response.Code != status || body != expected {
		t.Fatalf("expected stable %d %#v, got %d: %s", status, expected, response.Code, response.Body.String())
	}
	if requestID != "" && response.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("expected response request ID %q, got %q", requestID, response.Header().Get("X-Request-ID"))
	}
	if response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), "private Git diagnostic") || strings.Contains(response.Body.String(), "partial diff bytes") {
		t.Fatalf("failed archive must not expose a result or internal bytes: %s", response.Body.String())
	}
}

func assertArtifactMissingForTest(t *testing.T, handler http.Handler, tenantID string) {
	t.Helper()
	for _, suffix := range []string{"", "/content"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-1"+suffix+"?tenantId="+tenantID, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("rejected archive must not leave a readable Artifact: %d: %s", response.Code, response.Body.String())
		}
	}
}

func assertArtifactRetryForTest(t *testing.T, handler http.Handler, ready workspace.Workspace, key, requestID string) {
	t.Helper()
	before := preparationEventsForTest(t, handler, ready)
	response := postDiffArchiveForTest(handler, context.Background(), ready, key, requestID)
	if response.Code != http.StatusCreated {
		t.Fatalf("corrected archive using the same key must create an Artifact: %d: %s", response.Code, response.Body.String())
	}
	var archived artifact.Artifact
	if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
		t.Fatalf("decode corrected archive: %v", err)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(artifactBoundaryPatch)))
	if archived.ID != "artifact-1" || archived.TenantID != ready.TenantID || archived.TaskID != ready.TaskID || archived.WorkspaceID != ready.ID || archived.Type != artifact.TypeRepositoryDiff || archived.MediaType != "text/x-diff" || archived.SHA256 != wantSHA || archived.SizeBytes != int64(len(artifactBoundaryPatch)) {
		t.Fatalf("expected the first Artifact to contain the complete diff, got %#v", archived)
	}
	location := "/api/v1/artifacts/" + archived.ID
	if response.Header().Get("Location") != location || response.Header().Get("X-Request-ID") != requestID {
		t.Fatal("corrected archive must identify its result and request")
	}
	content := httptest.NewRecorder()
	handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, location+"/content?tenantId="+ready.TenantID, nil))
	if content.Code != http.StatusOK || content.Body.String() != artifactBoundaryPatch || content.Header().Get("ETag") != `"`+wantSHA+`"` {
		t.Fatalf("retry must save the complete diff: %d: %s", content.Code, content.Body.String())
	}
	replay := postDiffArchiveForTest(handler, context.Background(), ready, key, requestID+"-replay")
	if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
		t.Fatalf("corrected archive must be replayable: %d: %s", replay.Code, replay.Body.String())
	}
	events := preparationEventsForTest(t, handler, ready)
	if len(events) != len(before)+1 || !reflect.DeepEqual(events[:len(before)], before) {
		t.Fatal("retry must append exactly one event and preserve earlier facts")
	}
	event := events[len(before)]
	payload := event.Payload.Artifact
	if event.EventType != task.EventTypeArtifactCreated || event.Sequence != uint64(len(before)+1) || event.CausationID != requestID || event.TaskID != ready.TaskID || event.TenantID != ready.TenantID || payload == nil || payload.ArtifactID != archived.ID || payload.WorkspaceID != ready.ID || payload.SHA256 != wantSHA || payload.SizeBytes != archived.SizeBytes {
		t.Fatalf("creation event must belong to the corrected request: %#v", event)
	}
	if getPreparationWorkspaceForTest(t, handler, ready) != ready {
		t.Fatal("retry must preserve the READY Workspace")
	}
}
