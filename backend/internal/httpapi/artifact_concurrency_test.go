package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestConcurrentDiffArchiveKeepsArtifactsAndEventsConsistent(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		anotherWorkspace bool
		secondTenant     string
		secondKey        string
		differentContent bool
		otherStatus      int
	}{
		{"same key and content", false, "tenant-a", "shared-archive", false, http.StatusOK},
		{"same key with different content", false, "tenant-a", "shared-archive", true, http.StatusConflict},
		{"different keys with the same content", false, "tenant-a", "other-archive", false, http.StatusCreated},
		{"same tenant and key with different Tasks", true, "tenant-a", "shared-archive", false, http.StatusConflict},
		{"different tenants with the same key", true, "tenant-b", "shared-archive", false, http.StatusCreated},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const firstPatch = "diff --git a/review.md b/review.md\n-old\n+first\n"
			const secondPatch = "diff --git a/review.md b/review.md\n-old\n+second\n"
			contentBySHA := make(map[string]string)
			for _, patch := range []string{firstPatch, secondPatch} {
				contentBySHA[fmt.Sprintf("%x", sha256.Sum256([]byte(patch)))] = patch
			}
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var reads atomic.Uint64
			var replayContent atomic.Value
			replayContent.Store(firstPatch)
			handler, err := NewHandlerWithWorkspaceServices(
				repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
				workspacePreparerFunc(func(context.Context, task.RepositoryReference, string) error { return nil }),
				diffReaderFunc(func(ctx context.Context, _ repository.DiffInput) ([]byte, error) {
					call := reads.Add(1)
					if call <= 2 {
						entered <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if scenario.differentContent && call == 2 {
							return []byte(secondPatch), nil
						}
						return []byte(firstPatch), nil
					}
					return []byte(replayContent.Load().(string)), nil
				}),
				t.TempDir(),
				"",
			)
			if err != nil {
				t.Fatalf("create handler: %v", err)
			}
			first := readyWorkspaceForArtifactTest(t, handler, "tenant-a", "first-artifact")
			second := first
			if scenario.anotherWorkspace {
				second = readyWorkspaceForArtifactTest(t, handler, scenario.secondTenant, "second-artifact")
			}
			commands := []struct {
				workspace workspace.Workspace
				key       string
				requestID string
			}{
				{first, "shared-archive", "req-concurrent-archive-1"},
				{second, scenario.secondKey, "req-concurrent-archive-2"},
			}
			type outcome struct {
				index    int
				response *httptest.ResponseRecorder
			}
			outcomes := make(chan outcome, 2)
			finished := make(chan struct{}, 2)
			for index, command := range commands {
				go func() {
					defer func() { finished <- struct{}{} }()
					outcomes <- outcome{index, postDiffArchiveForTest(handler, ctx, command.workspace, command.key, command.requestID)}
				}()
			}
			defer func() {
				unblock()
				cancel()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for range commands {
					select {
					case <-finished:
					case <-timer.C:
						t.Error("archive goroutines did not stop during cleanup")
						return
					}
				}
			}()
			// 两次 diff 读取都已开始才放行，让请求竞争同一个归档键。
			for range commands {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("both archive requests must reach diff reading")
				}
			}
			for _, current := range map[string]workspace.Workspace{first.ID: first, second.ID: second} {
				if len(preparationEventsForTest(t, handler, current)) != 5 {
					t.Fatal("blocked diff reading must not append an Artifact event")
				}
			}
			unblock()
			responses := make([]*httptest.ResponseRecorder, len(commands))
			for range commands {
				select {
				case result := <-outcomes:
					responses[result.index] = result.response
				case <-ctx.Done():
					t.Fatal("concurrent archive requests did not finish")
				}
			}
			type creation struct {
				metadata artifact.Artifact
				response *httptest.ResponseRecorder
				index    int
			}
			created := make(map[string]creation)
			counts := make(map[int]int)
			wantCounts := map[int]int{http.StatusCreated: 1}
			wantCounts[scenario.otherStatus]++
			for index, response := range responses {
				command := commands[index]
				counts[response.Code]++
				if wantCounts[response.Code] == 0 || response.Header().Get("X-Request-ID") != command.requestID {
					t.Fatalf("unexpected archive response: %d: %s", response.Code, response.Body.String())
				}
				if response.Code == http.StatusConflict {
					var body errorResponse
					if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
						t.Fatalf("decode archive conflict: %v", err)
					}
					if body.Error != "idempotency_conflict" {
						t.Fatalf("expected idempotency_conflict, got %s", response.Body.String())
					}
					continue
				}
				var archived artifact.Artifact
				if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
					t.Fatalf("decode Artifact: %v", err)
				}
				patch, knownSHA := contentBySHA[archived.SHA256]
				if archived.ID == "" || archived.TaskID != command.workspace.TaskID || archived.TenantID != command.workspace.TenantID || archived.WorkspaceID != command.workspace.ID || archived.Type != artifact.TypeRepositoryDiff || archived.MediaType != "text/x-diff" || !knownSHA || archived.SizeBytes != int64(len(patch)) || archived.CreatedAt.IsZero() {
					t.Fatalf("unexpected Artifact ownership or metadata: %#v", archived)
				}
				if !scenario.differentContent && patch != firstPatch {
					t.Fatalf("expected the fixed diff content, got SHA %s", archived.SHA256)
				}
				if response.Header().Get("Location") != "/api/v1/artifacts/"+archived.ID {
					t.Fatalf("unexpected Artifact Location: %q", response.Header().Get("Location"))
				}
				if response.Code == http.StatusCreated {
					if _, exists := created[archived.ID]; exists {
						t.Fatal("distinct creations must have distinct Artifact IDs")
					}
					created[archived.ID] = creation{archived, response, index}
					if scenario.differentContent {
						// 后续重放让外部读取器返回已归档的赢家内容。
						replayContent.Store(patch)
					}
				}
			}
			for status, count := range wantCounts {
				if counts[status] != count {
					t.Fatalf("expected response counts %v, got %v", wantCounts, counts)
				}
			}
			for _, response := range responses {
				if response.Code == http.StatusOK {
					var replayed artifact.Artifact
					if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil {
						t.Fatalf("decode concurrent replay: %v", err)
					}
					winner, exists := created[replayed.ID]
					if !exists || response.Body.String() != winner.response.Body.String() {
						t.Fatal("concurrent replay must return the winning Artifact metadata")
					}
				}
			}
			for _, winner := range created {
				metadata := winner.metadata
				location := winner.response.Header().Get("Location")
				get := httptest.NewRecorder()
				handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, location+"?tenantId="+metadata.TenantID, nil).WithContext(ctx))
				if get.Code != http.StatusOK || get.Body.String() != winner.response.Body.String() {
					t.Fatalf("GET must return the saved Artifact: %d: %s", get.Code, get.Body.String())
				}
				content := httptest.NewRecorder()
				handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, location+"/content?tenantId="+metadata.TenantID, nil).WithContext(ctx))
				if content.Code != http.StatusOK || content.Body.String() != contentBySHA[metadata.SHA256] || content.Header().Get("ETag") != `"`+metadata.SHA256+`"` || content.Header().Get("Content-Type") != metadata.MediaType || content.Header().Get("Content-Length") != fmt.Sprint(metadata.SizeBytes) || content.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("stored content and headers must match the winning metadata: %d: %s", content.Code, content.Body.String())
				}
				otherTenant := "tenant-b"
				if metadata.TenantID == otherTenant {
					otherTenant = "tenant-a"
				}
				for _, suffix := range []string{"", "/content"} {
					hidden := httptest.NewRecorder()
					handler.ServeHTTP(hidden, httptest.NewRequest(http.MethodGet, location+suffix+"?tenantId="+otherTenant, nil).WithContext(ctx))
					if hidden.Code != http.StatusNotFound {
						t.Fatalf("another tenant must not read this Artifact: %d: %s", hidden.Code, hidden.Body.String())
					}
				}
				command := commands[winner.index]
				replay := postDiffArchiveForTest(handler, ctx, command.workspace, command.key, fmt.Sprintf("req-archive-replay-%d", winner.index))
				if replay.Code != http.StatusOK || replay.Body.String() != winner.response.Body.String() {
					t.Fatalf("successful archive must remain replayable: %d: %s", replay.Code, replay.Body.String())
				}
			}
			for _, current := range map[string]workspace.Workspace{first.ID: first, second.ID: second} {
				if found := getPreparationWorkspaceForTest(t, handler, current); found != current {
					t.Fatalf("archiving must preserve the READY Workspace: %#v", found)
				}
				wantCreated := 0
				for _, winner := range created {
					if winner.metadata.TaskID == current.TaskID {
						wantCreated++
					}
				}
				events := preparationEventsForTest(t, handler, current)
				if len(events) != 5+wantCreated {
					t.Fatalf("expected %d Task events, got %d", 5+wantCreated, len(events))
				}
				seen := make(map[string]bool)
				for index, event := range events[5:] {
					payload := event.Payload.Artifact
					if payload == nil {
						t.Fatalf("expected an Artifact event payload: %#v", event)
					}
					winner, exists := created[payload.ArtifactID]
					metadata := winner.metadata
					if !exists || seen[metadata.ID] || metadata.TaskID != current.TaskID || event.TenantID != current.TenantID || event.TaskID != current.TaskID || event.EventType != task.EventTypeArtifactCreated || event.Sequence != uint64(index+6) || event.CausationID != commands[winner.index].requestID || !event.OccurredAt.Equal(metadata.CreatedAt) || payload.WorkspaceID != metadata.WorkspaceID || payload.Type != string(metadata.Type) || payload.MediaType != metadata.MediaType || payload.SHA256 != metadata.SHA256 || payload.SizeBytes != metadata.SizeBytes {
						t.Fatalf("Artifact event must match its unique creation: %#v", event)
					}
					seen[metadata.ID] = true
				}
			}
		})
	}
}

func readyWorkspaceForArtifactTest(t *testing.T, handler http.Handler, tenantID, key string) workspace.Workspace {
	t.Helper()
	registered := registerPreparationWorkspaceForTest(t, handler, tenantID, key)
	prepared := postWorkspacePreparationForTest(handler, context.Background(), registered, "prepare-"+key, "req-prepare-"+key, registered.Version)
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare Workspace: %d: %s", prepared.Code, prepared.Body.String())
	}
	var ready workspace.Workspace
	if err := json.Unmarshal(prepared.Body.Bytes(), &ready); err != nil {
		t.Fatalf("decode READY Workspace: %v", err)
	}
	if ready.State != workspace.StateReady || ready.Version != 3 || ready.Path == "" {
		t.Fatalf("expected READY version 3, got %#v", ready)
	}
	return ready
}

func postDiffArchiveForTest(handler http.Handler, ctx context.Context, current workspace.Workspace, key, requestID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+current.TaskID+"/artifacts/diff", strings.NewReader(fmt.Sprintf(`{
		"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"expectedWorkspaceVersion":%d
	}`, requestID, key, current.TenantID, current.Version))).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
