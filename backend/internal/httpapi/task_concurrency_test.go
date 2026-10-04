package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/task"
)

func TestConcurrentCreateTaskKeepsOneTaskSnapshotAndEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var resolveCalls atomic.Uint64
	handler := newHandlerWithTestResolver(repositoryResolverFunc(func(ctx context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
		call := resolveCalls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return task.RepositoryReference{}, ctx.Err()
		}
		selection.TargetBranch = "master"
		// 两次 GitLab 读取可以看到不同 master；最终只能固定其中一份完整快照。
		selection.TargetSHA = strings.Repeat(fmt.Sprint(call+2), 40)
		selection.BaseSHA = strings.Repeat(fmt.Sprint(call), 40)
		return selection, nil
	}))
	responses := make(chan *httptest.ResponseRecorder, 2)
	for index := range 2 {
		body := fmt.Sprintf(`{
			"requestId":"req-concurrent-create-%d",
			"idempotencyKey":"concurrent-create",
			"tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review concurrently",
			%s
		}`, index, testRepositoryJSON)
		go func() {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)).WithContext(ctx)
			handler.ServeHTTP(response, request)
			responses <- response
		}()
	}
	// 两个请求都越过幂等预查并开始外部解析后才放行；不用 sleep 猜交错顺序。
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both create requests must reach repository resolution")
		}
	}
	close(release)
	var created, replayed *httptest.ResponseRecorder
	for range 2 {
		select {
		case response := <-responses:
			switch response.Code {
			case http.StatusCreated:
				if created != nil {
					t.Fatal("concurrent requests created more than one Task")
				}
				created = response
			case http.StatusOK:
				replayed = response
			default:
				t.Fatalf("unexpected create response: %d: %s", response.Code, response.Body.String())
			}
		case <-ctx.Done():
			t.Fatal("concurrent create requests did not finish")
		}
	}
	if created == nil || replayed == nil || !bytes.Equal(created.Body.Bytes(), replayed.Body.Bytes()) {
		t.Fatal("creation and replay must return the same immutable Task snapshot")
	}
	var result task.Task
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode created Task: %v", err)
	}
	if result.ID != "task-1" || result.Version != 1 || result.Status != task.StatusCreated || resolveCalls.Load() != 2 {
		t.Fatalf("unexpected concurrent creation result: %#v", result)
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?tenantId=tenant-a", nil))
	var tasks struct {
		Items []task.Task `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode Task list: %v", err)
	}
	if list.Code != http.StatusOK || len(tasks.Items) != 1 || tasks.Items[0].Repository != result.Repository {
		t.Fatalf("expected one Task with the winning snapshot, got %d: %s", list.Code, list.Body.String())
	}
	events := taskEventItemsForTest(t, handler)
	if len(events) != 1 {
		t.Fatalf("expected one creation event, got %d", len(events))
	}
	var event task.Event
	if err := json.Unmarshal(events[0], &event); err != nil {
		t.Fatalf("decode creation event: %v", err)
	}
	if event.EventType != task.EventTypeTaskCreated || event.Sequence != 1 || event.CausationID != created.Header().Get("X-Request-ID") {
		t.Fatalf("creation event must belong to the request that created the Task: %#v", event)
	}
}

func TestConcurrentQueueTaskUpdatesTheVersionAndEventOnce(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		name := "different idempotency keys"
		if sameKey {
			name = "same idempotency key"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			handler := newHandlerWithVerifiedRepositories()
			created := httptest.NewRecorder()
			handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{
				"requestId":"req-before-concurrent-queue","idempotencyKey":"before-concurrent-queue",
				"tenantId":"tenant-a","type":"PR_REVIEW","goal":"Review concurrently",
				`+testRepositoryJSON+`
			}`)))
			if created.Code != http.StatusCreated {
				t.Fatalf("create Task: %d: %s", created.Code, created.Body.String())
			}
			const requestCount = 16
			ready := make(chan struct{}, requestCount)
			start := make(chan struct{})
			responses := make(chan *httptest.ResponseRecorder, requestCount)
			for index := range requestCount {
				key := fmt.Sprintf("queue-%d", index)
				if sameKey {
					key = "queue-shared"
				}
				body := fmt.Sprintf(`{
					"requestId":"req-concurrent-queue-%d","idempotencyKey":%q,
					"tenantId":"tenant-a","expectedVersion":1,"status":"QUEUED"
				}`, index, key)
				go func() {
					request := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task-1", strings.NewReader(body)).WithContext(ctx)
					response := httptest.NewRecorder()
					ready <- struct{}{}
					select {
					case <-start:
						handler.ServeHTTP(response, request)
						responses <- response
					case <-ctx.Done():
					}
				}()
			}
			for range requestCount {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("queue requests did not become ready")
				}
			}
			close(start)
			succeeded := make(map[string]bool)
			conflicts := 0
			for range requestCount {
				select {
				case response := <-responses:
					switch response.Code {
					case http.StatusOK:
						var queued task.Task
						if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
							t.Fatalf("decode queued Task: %v", err)
						}
						if queued.ID != "task-1" || queued.Status != task.StatusQueued || queued.Version != 2 {
							t.Fatalf("expected one version increment: %#v", queued)
						}
						succeeded[response.Header().Get("X-Request-ID")] = true
					case http.StatusConflict:
						var failure errorResponse
						if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
							t.Fatalf("decode queue conflict: %v", err)
						}
						if failure.Error != "version_conflict" {
							t.Fatalf("expected version_conflict, got %#v", failure)
						}
						conflicts++
					default:
						t.Fatalf("unexpected queue response: %d: %s", response.Code, response.Body.String())
					}
				case <-ctx.Done():
					t.Fatal("queue requests did not finish")
				}
			}
			wantSucceeded, wantConflicts := 1, requestCount-1
			if sameKey {
				wantSucceeded, wantConflicts = requestCount, 0
			}
			if len(succeeded) != wantSucceeded || conflicts != wantConflicts {
				t.Fatalf("expected %d successes and %d conflicts, got %d and %d", wantSucceeded, wantConflicts, len(succeeded), conflicts)
			}
			current := httptest.NewRecorder()
			handler.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1?tenantId=tenant-a", nil))
			var queued task.Task
			if err := json.Unmarshal(current.Body.Bytes(), &queued); err != nil {
				t.Fatalf("decode current Task: %v", err)
			}
			if current.Code != http.StatusOK || queued.Status != task.StatusQueued || queued.Version != 2 {
				t.Fatalf("unexpected final Task: %d: %s", current.Code, current.Body.String())
			}
			events := taskEventItemsForTest(t, handler)
			if len(events) != 2 {
				t.Fatalf("expected creation plus one queue event, got %d", len(events))
			}
			var event task.Event
			if err := json.Unmarshal(events[1], &event); err != nil {
				t.Fatalf("decode queue event: %v", err)
			}
			if event.EventType != task.EventTypeTaskQueued || event.Sequence != 2 || !succeeded[event.CausationID] || event.Payload.Task == nil || event.Payload.Task.Version != 2 {
				t.Fatalf("queue event must describe the single successful transition: %#v", event)
			}
		})
	}
}
