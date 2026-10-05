package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestConcurrentWorkspaceRegistrationKeepsTaskAndKeyBindings(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		anotherTask  bool
		secondTenant string
		secondKey    string
		otherStatus  int
		conflictCode string
	}{
		{"same Task and key", false, "tenant-a", "shared-register", http.StatusOK, ""},
		{"same Task with different keys", false, "tenant-a", "other-register", http.StatusConflict, "workspace_already_exists"},
		{"same tenant and key with different Tasks", true, "tenant-a", "shared-register", http.StatusConflict, "idempotency_conflict"},
		{"different tenants with the same key", true, "tenant-b", "shared-register", http.StatusCreated, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var verificationCalls atomic.Uint64
			handler := NewHandlerWithRepositoryServices(repositoryVerifierFunc(
				func(ctx context.Context, _ task.RepositoryReference) error {
					if verificationCalls.Add(1) <= 2 {
						entered <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				},
			), "")
			firstTask := createQueuedTaskForTest(t, handler, "tenant-a", "first-registration")
			secondTask := firstTask
			if scenario.anotherTask {
				secondTask = createQueuedTaskForTest(t, handler, scenario.secondTenant, "second-registration")
			}
			commands := []struct {
				task      task.Task
				key       string
				requestID string
			}{
				{firstTask, "shared-register", "req-concurrent-register-1"},
				{secondTask, scenario.secondKey, "req-concurrent-register-2"},
			}
			register := func(current task.Task, key, requestID string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+current.ID+"/workspace", strings.NewReader(fmt.Sprintf(`{
					"requestId":%q,"idempotencyKey":%q,"tenantId":%q
				}`, requestID, key, current.TenantID))).WithContext(ctx)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
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
					outcomes <- outcome{index, register(command.task, command.key, command.requestID)}
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
						t.Error("registration goroutines did not stop during cleanup")
						return
					}
				}
			}()
			// 两次外部校验都开始后才放行，确定覆盖校验后的竞争，不用 sleep 猜顺序。
			for range commands {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("both registration requests must reach repository verification")
				}
			}
			pending := httptest.NewRecorder()
			handler.ServeHTTP(pending, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+firstTask.ID+"/workspace?tenantId="+firstTask.TenantID, nil).WithContext(ctx))
			if pending.Code != http.StatusNotFound || len(taskEventItemsForTest(t, handler)) != 2 {
				t.Fatal("blocked verification must leave the Workspace absent and the timeline unchanged")
			}
			unblock()
			responses := make([]*httptest.ResponseRecorder, len(commands))
			for range commands {
				select {
				case result := <-outcomes:
					responses[result.index] = result.response
				case <-ctx.Done():
					t.Fatal("concurrent registration requests did not finish")
				}
			}
			wantCounts := map[int]int{http.StatusCreated: 1}
			wantCounts[scenario.otherStatus]++
			counts := make(map[int]int)
			createdByTask := make(map[string]int)
			workspaceIDs := make(map[string]bool)
			for index, response := range responses {
				command := commands[index]
				counts[response.Code]++
				if wantCounts[response.Code] == 0 || response.Header().Get("X-Request-ID") != command.requestID {
					t.Fatalf("unexpected registration response: %d: %s", response.Code, response.Body.String())
				}
				if response.Code == http.StatusConflict {
					var body errorResponse
					if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
						t.Fatalf("decode registration conflict: %v", err)
					}
					if body.Error != scenario.conflictCode {
						t.Fatalf("expected %s, got %s", scenario.conflictCode, response.Body.String())
					}
					continue
				}
				var registered workspace.Workspace
				if err := json.Unmarshal(response.Body.Bytes(), &registered); err != nil {
					t.Fatalf("decode registered Workspace: %v", err)
				}
				if registered.ID == "" || registered.TaskID != command.task.ID || registered.TenantID != command.task.TenantID || registered.State != workspace.StateRegistered || registered.Version != 1 || registered.Path != "" {
					t.Fatalf("unexpected Workspace identity or state: %#v", registered)
				}
				if response.Header().Get("Location") != "/api/v1/tasks/"+command.task.ID+"/workspace" {
					t.Fatalf("unexpected Workspace Location: %q", response.Header().Get("Location"))
				}
				if response.Code == http.StatusCreated {
					if _, exists := createdByTask[command.task.ID]; exists || workspaceIDs[registered.ID] {
						t.Fatal("concurrent registration must create one Workspace per Task with distinct IDs")
					}
					createdByTask[command.task.ID] = index
					workspaceIDs[registered.ID] = true
				}
			}
			for status, count := range wantCounts {
				if counts[status] != count {
					t.Fatalf("expected response counts %v, got %v", wantCounts, counts)
				}
			}
			// 完成后的重放仍须找到本 Task 的赢家，不能被另一个请求覆盖幂等索引。
			for index, response := range responses {
				command := commands[index]
				if response.Code == http.StatusOK {
					winner := responses[createdByTask[command.task.ID]]
					if response.Body.String() != winner.Body.String() {
						t.Fatal("concurrent replay must return the winning Workspace snapshot")
					}
				}
				if response.Code == http.StatusCreated {
					replay := register(command.task, command.key, fmt.Sprintf("req-register-replay-%d", index))
					if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
						t.Fatalf("successful registration must remain replayable: %d: %s", replay.Code, replay.Body.String())
					}
				}
			}
			if verificationCalls.Load() != 2 {
				t.Fatalf("completed replays must not repeat repository verification: %d calls", verificationCalls.Load())
			}
			for _, current := range map[string]task.Task{firstTask.ID: firstTask, secondTask.ID: secondTask} {
				winnerIndex, exists := createdByTask[current.ID]
				get := httptest.NewRecorder()
				handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+current.ID+"/workspace?tenantId="+current.TenantID, nil).WithContext(ctx))
				wantEvents := 2
				if exists {
					wantEvents = 3
					if get.Code != http.StatusOK || get.Body.String() != responses[winnerIndex].Body.String() {
						t.Fatalf("GET must return the winning Workspace: %d: %s", get.Code, get.Body.String())
					}
				} else if get.Code != http.StatusNotFound {
					t.Fatalf("rejected registration must leave no Workspace: %d: %s", get.Code, get.Body.String())
				}
				eventsResponse := httptest.NewRecorder()
				handler.ServeHTTP(eventsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+current.ID+"/events?tenantId="+current.TenantID, nil).WithContext(ctx))
				var events struct {
					Items []task.Event `json:"items"`
				}
				if err := json.Unmarshal(eventsResponse.Body.Bytes(), &events); err != nil {
					t.Fatalf("decode Task events: %v", err)
				}
				if eventsResponse.Code != http.StatusOK || len(events.Items) != wantEvents {
					t.Fatalf("expected %d events, got %d: %s", wantEvents, eventsResponse.Code, eventsResponse.Body.String())
				}
				if exists {
					var registered workspace.Workspace
					if err := json.Unmarshal(get.Body.Bytes(), &registered); err != nil {
						t.Fatalf("decode Workspace GET: %v", err)
					}
					event := events.Items[2]
					payload := event.Payload.Workspace
					if event.EventType != task.EventTypeWorkspaceRegistered || event.Sequence != 3 || event.CausationID != commands[winnerIndex].requestID || payload == nil || payload.WorkspaceID != registered.ID || payload.State != string(workspace.StateRegistered) || payload.Version != 1 {
						t.Fatalf("registration event must belong to the winning request: %#v", event)
					}
				}
			}
		})
	}
}
