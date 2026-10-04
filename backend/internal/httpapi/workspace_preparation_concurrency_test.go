package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestPrepareWorkspaceReservesItsIdempotencyKeyBeforeCallingPreparer(t *testing.T) {
	for _, secondTenant := range []string{"tenant-a", "tenant-b"} {
		t.Run(secondTenant, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Uint64
			handler, err := NewHandlerWithWorkspacePreparer(
				repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }),
				workspacePreparerFunc(func(ctx context.Context, _ task.RepositoryReference, _ string) error {
					if calls.Add(1) == 1 {
						started <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				}),
				t.TempDir(),
				"",
			)
			if err != nil {
				t.Fatalf("create handler: %v", err)
			}
			first := registerPreparationWorkspaceForTest(t, handler, "tenant-a", "first")
			second := registerPreparationWorkspaceForTest(t, handler, secondTenant, "second")
			responses := make(chan *httptest.ResponseRecorder, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				responses <- postWorkspacePreparationForTest(handler, ctx, first, "shared-prepare", "req-first", 1)
			}()
			defer func() {
				unblock()
				cancel()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("preparation goroutine did not stop during cleanup")
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first request did not reach the preparer")
			}
			current := getPreparationWorkspaceForTest(t, handler, first)
			if current.State != workspace.StatePreparing || current.Version != 2 || current.Path != "" {
				t.Fatalf("expected visible PREPARING version 2 while Git is blocked, got %#v", current)
			}

			// 同一个租户的 key 必须在外部 I/O 之前绑定 Task；不同租户的 key 互不占用。
			competing := postWorkspacePreparationForTest(handler, ctx, second, "shared-prepare", "req-second", 1)
			wantCalls := uint64(2)
			wantState, wantVersion, wantEvents := workspace.StateReady, uint64(3), 5
			if secondTenant == first.TenantID {
				assertPreparationConflictForTest(t, competing, "idempotency_conflict")
				wantCalls = 1
				wantState, wantVersion, wantEvents = workspace.StateRegistered, 1, 3
			} else if competing.Code != http.StatusOK {
				t.Fatalf("another tenant must be able to use the same key: %d: %s", competing.Code, competing.Body.String())
			}
			other := getPreparationWorkspaceForTest(t, handler, second)
			if other.State != wantState || other.Version != wantVersion || len(preparationEventsForTest(t, handler, second)) != wantEvents {
				t.Fatalf("unexpected competing Workspace state or events: %#v", other)
			}
			if secondTenant == first.TenantID && other.Path != "" {
				t.Fatalf("rejected request must not expose a worktree path: %q", other.Path)
			}
			if calls.Load() != wantCalls {
				t.Fatalf("expected %d preparations, got %d", wantCalls, calls.Load())
			}

			// 原请求尚未完成时，同输入不能重复开始；改版本也不能改变 key 的绑定。
			assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, first, "shared-prepare", "req-inflight-replay", 1), "version_conflict")
			assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, first, "shared-prepare", "req-change-version", 2), "idempotency_conflict")
			assertPreparationConflictForTest(t, postWorkspacePreparationForTest(handler, ctx, first, "another-prepare", "req-new-key", 2), "invalid_workspace_state")
			if calls.Load() != wantCalls || len(preparationEventsForTest(t, handler, first)) != 4 {
				t.Fatal("in-flight conflicts must not call the preparer or append events")
			}
			unblock()
			var prepared *httptest.ResponseRecorder
			select {
			case prepared = <-responses:
			case <-ctx.Done():
				t.Fatal("first preparation did not finish")
			}
			if prepared.Code != http.StatusOK {
				t.Fatalf("first preparation failed: %d: %s", prepared.Code, prepared.Body.String())
			}
			ready := getPreparationWorkspaceForTest(t, handler, first)
			if ready.State != workspace.StateReady || ready.Version != 3 || ready.Path == "" {
				t.Fatalf("expected original Workspace READY at version 3, got %#v", ready)
			}
			replay := postWorkspacePreparationForTest(handler, ctx, first, "shared-prepare", "req-completed-replay", 1)
			if replay.Code != http.StatusOK || !bytes.Equal(replay.Body.Bytes(), prepared.Body.Bytes()) {
				t.Fatalf("successful replay must return the original snapshot: %d: %s", replay.Code, replay.Body.String())
			}
			events := preparationEventsForTest(t, handler, first)
			if calls.Load() != wantCalls || len(events) != 5 {
				t.Fatal("successful replay must not call the preparer or append events")
			}
			for _, event := range events[3:] {
				if event.CausationID != "req-first" {
					t.Fatalf("preparation events must belong to the original request: %#v", event)
				}
			}
		})
	}
}
