package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

func TestReviewExecutionArchivesFindingsThroughRealGitCLIAndHTTP(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	server := httptest.NewServer(fixture.handler)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second
	address := server.URL + "/api/v1/tasks/" + fixture.ready.TaskID + "/review"
	content, headers := requestReviewInputTest(t, client, address, http.MethodPost,
		fixture.body("review-first", "req-review-first", fixture.ready.Version), http.StatusCreated)
	var result struct {
		ID               string            `json:"id"`
		TenantID         string            `json:"tenantId"`
		TaskID           string            `json:"taskId"`
		WorkspaceID      string            `json:"workspaceId"`
		WorkspaceVersion uint64            `json:"workspaceVersion"`
		BaseSHA          string            `json:"baseSha"`
		HeadSHA          string            `json:"headSha"`
		State            string            `json:"state"`
		Artifact         artifact.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || result.State != "SUCCEEDED" || result.TenantID != fixture.ready.TenantID || result.TaskID != fixture.ready.TaskID || result.WorkspaceID != fixture.ready.ID || result.WorkspaceVersion != fixture.ready.Version || result.BaseSHA != fixture.ready.BaseSHA || result.HeadSHA != fixture.ready.HeadSHA {
		t.Fatalf("unexpected execution coordinates: %s", content)
	}
	metadata := result.Artifact
	if metadata.Type != artifact.Type("PR_REVIEW_FINDINGS") || metadata.MediaType != "application/json" || metadata.TenantID != fixture.ready.TenantID || metadata.TaskID != fixture.ready.TaskID || metadata.WorkspaceID != fixture.ready.ID {
		t.Fatalf("unexpected Findings Artifact: %+v", metadata)
	}
	stored, storedHeaders := requestReviewInputTest(t, client, server.URL+"/api/v1/artifacts/"+metadata.ID+"/content?tenantId="+fixture.ready.TenantID, http.MethodGet, "", http.StatusOK)
	parsed, err := review.ParseFindings(stored, fixture.ready.BaseSHA, fixture.ready.HeadSHA)
	if err != nil || len(parsed.Findings) != 1 {
		t.Fatalf("expected validated and deduplicated Findings: %s, %v", stored, err)
	}
	if metadata.SHA256 != fmt.Sprintf("%x", sha256.Sum256(stored)) || metadata.SizeBytes != int64(len(stored)) || storedHeaders.Get("ETag") != `"`+metadata.SHA256+`"` {
		t.Fatal("Findings content differs from its Artifact metadata")
	}
	captures := fixture.captures(t)
	if len(captures) != 1 || captures[0].CWD != fixture.ready.Path || captures[0].Input.BaseSHA != fixture.ready.BaseSHA || captures[0].Input.HeadSHA != fixture.ready.HeadSHA || !strings.Contains(captures[0].Input.Patch, "+feature-only change") {
		t.Fatalf("Runner did not receive the fixed Git input: %+v", captures)
	}
	if headers.Get("X-Request-ID") != "req-review-first" || headers.Get("Location") != "/api/v1/tasks/"+fixture.ready.TaskID+"/review?tenantId="+fixture.ready.TenantID {
		t.Fatalf("unexpected execution headers: %v", headers)
	}
	latest, _ := requestReviewInputTest(t, client, server.URL+headers.Get("Location"), http.MethodGet, "", http.StatusOK)
	if string(latest) != string(content) {
		t.Fatal("GET must return the saved execution projection")
	}
	events := preparationEventsForTest(t, fixture.handler, fixture.ready)
	want := []task.EventType{task.EventType("review.started"), task.EventTypeArtifactCreated, task.EventType("review.succeeded")}
	if len(events) != 8 {
		t.Fatalf("expected 8 events, got %+v", events)
	}
	for index, event := range events[5:] {
		if event.EventType != want[index] || event.Sequence != uint64(index+6) || event.CausationID != "req-review-first" {
			t.Fatalf("unexpected review event: %+v", event)
		}
	}
	requestReviewInputTest(t, client, address+"?tenantId=other-tenant", http.MethodGet, "", http.StatusNotFound)
	requestReviewInputTest(t, client, server.URL+"/api/v1/artifacts/"+metadata.ID+"/content?tenantId=other-tenant", http.MethodGet, "", http.StatusNotFound)
}

func TestReviewExecutionReplaysBeforeReadingGitOrLaunchingCLI(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	first := fixture.post(context.Background(), "review-replay", "req-first", fixture.ready.Version)
	if first.Code != http.StatusCreated {
		t.Fatalf("first execution: %d %s", first.Code, first.Body.String())
	}
	if err := os.Rename(fixture.ready.Path, filepath.Join(t.TempDir(), "moved-worktree")); err != nil {
		t.Fatal(err)
	}
	replay := fixture.post(context.Background(), "review-replay", "req-replay", fixture.ready.Version)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() || replay.Header().Get("X-Request-ID") != "req-replay" {
		t.Fatalf("replay must use the saved result without Git or CLI: %d %s", replay.Code, replay.Body.String())
	}
	conflict := fixture.post(context.Background(), "review-replay", "req-conflict", fixture.ready.Version+1)
	assertReviewError(t, conflict, http.StatusConflict, "idempotency_conflict")
	if len(fixture.captures(t)) != 1 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 8 {
		t.Fatal("replays and conflicts must not execute or append events")
	}
}

func TestReviewExecutionRejectsConcurrentKeysForTheSameTask(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "hang", 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- fixture.post(ctx, "active-key", "req-active", fixture.ready.Version) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("active execution did not finish")
		}
	})
	fixture.waitStarted(t)
	get := httptest.NewRecorder()
	fixture.handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+fixture.ready.TaskID+"/review?tenantId="+fixture.ready.TenantID, nil))
	var running review.Execution
	if err := json.Unmarshal(get.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	if get.Code != http.StatusOK || running.State != review.ExecutionRunning || running.Artifact != nil || !running.CompletedAt.IsZero() {
		t.Fatalf("expected RUNNING without result: %d %s", get.Code, get.Body.String())
	}
	for _, key := range []string{"active-key", "another-key"} {
		requestCtx, requestCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		response := fixture.post(requestCtx, key, "req-concurrent", fixture.ready.Version)
		requestCancel()
		assertReviewError(t, response, http.StatusConflict, "review_in_progress")
	}
	assertReviewError(t, fixture.post(context.Background(), "active-key", "req-conflicting-version", fixture.ready.Version+1), http.StatusConflict, "idempotency_conflict")
	if len(fixture.captures(t)) != 1 {
		t.Fatal("concurrent keys must not launch additional CLI processes")
	}
	if err := os.WriteFile(fixture.release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		if response.Code != http.StatusCreated {
			t.Fatalf("active execution: %d %s", response.Code, response.Body.String())
		}
		// Leave a completion for cleanup without waiting for a second process.
		done <- response
	case <-time.After(3 * time.Second):
		t.Fatal("active execution did not finish after release")
	}
	if len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 8 {
		t.Fatal("concurrent requests must not append extra events")
	}
}

func TestReviewExecutionSeparatesFindingsFromUserArchiveKeys(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	diff := postDiffArchiveForTest(fixture.handler, context.Background(), fixture.ready, "review:review-1", "req-user-archive")
	if diff.Code != http.StatusCreated {
		t.Fatalf("archive diff: %d %s", diff.Code, diff.Body.String())
	}
	result := fixture.post(context.Background(), "review-first", "req-review-first", fixture.ready.Version)
	if result.Code != http.StatusCreated {
		t.Fatalf("user archive keys must not collide with Findings: %d %s", result.Code, result.Body.String())
	}
	var operation review.Execution
	if err := json.Unmarshal(result.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	var archived artifact.Artifact
	if err := json.Unmarshal(diff.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if operation.Artifact == nil || operation.Artifact.ID == archived.ID {
		t.Fatal("diff and Findings need distinct Artifacts")
	}
	replayed := postDiffArchiveForTest(fixture.handler, context.Background(), fixture.ready, "review:review-1", "req-diff-replay")
	if replayed.Code != http.StatusOK || replayed.Body.String() != diff.Body.String() {
		t.Fatal("Findings must preserve diff idempotency")
	}
}

func TestReviewExecutionRecordsCancellationDuringGitInput(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "hang-diff", 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- fixture.post(ctx, "cancel-diff", "req-cancel-diff", fixture.ready.Version) }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(fixture.config + ".git-started"); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			cancel()
			t.Fatal("Git input did not start")
		}
	}
	assertReviewError(t, fixture.post(context.Background(), "another-key-during-input", "req-input-overlap", fixture.ready.Version), http.StatusConflict, "review_in_progress")
	cancel()
	select {
	case response := <-done:
		assertReviewError(t, response, http.StatusRequestTimeout, "review_canceled")
	case <-time.After(3 * time.Second):
		t.Fatal("Git cancellation did not finish")
	}
	if len(fixture.captures(t)) != 0 {
		t.Fatal("canceled input must not launch Codex")
	}
	operation := fixture.latest(t)
	if operation.State != review.ExecutionCanceled || operation.Artifact != nil || operation.ErrorCode != "review_canceled" {
		t.Fatalf("unexpected canceled projection: %+v", operation)
	}
}

func TestReviewExecutionRejectsRuntimeFieldsBeforeIO(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	for _, field := range []string{"binary", "model", "patch", "headSha", "sandbox", "credentials", "workspacePath"} {
		t.Run(field, func(t *testing.T) {
			body := strings.TrimSuffix(fixture.body("invalid-"+field, "req-invalid", fixture.ready.Version), "}") + fmt.Sprintf(`,%q:"untrusted"}`, field)
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+fixture.ready.TaskID+"/review", strings.NewReader(body)))
			assertReviewError(t, response, http.StatusBadRequest, "invalid_json")
		})
	}
	if len(fixture.captures(t)) != 0 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 5 {
		t.Fatal("invalid HTTP input must not run or create events")
	}
}

func TestReviewExecutionFailuresReplayAndAllowAnExplicitNewAttempt(t *testing.T) {
	for _, scenario := range []struct {
		mode, code string
		status     int
	}{
		{"nonzero", "review_execution_failed", http.StatusBadGateway},
		{"invalid", "review_findings_invalid", http.StatusBadGateway},
		{"wrong-head", "review_findings_invalid", http.StatusBadGateway},
		{"diff-unavailable", "diff_unavailable", http.StatusServiceUnavailable},
	} {
		t.Run(scenario.mode, func(t *testing.T) {
			fixture := newReviewExecutionFixture(t, scenario.mode, 5*time.Second)
			logs := captureJSONLogs(t)
			moved := filepath.Join(t.TempDir(), "moved")
			if scenario.mode == "diff-unavailable" {
				if err := os.Rename(fixture.ready.Path, moved); err != nil {
					t.Fatal(err)
				}
			}
			failed := fixture.post(context.Background(), "failure-key", "req-failure", fixture.ready.Version)
			assertReviewError(t, failed, scenario.status, scenario.code)
			operation := fixture.latest(t)
			if operation.State != review.ExecutionFailed || operation.ErrorCode != scenario.code || operation.Artifact != nil || operation.CompletedAt.IsZero() || operation.CompletedAt.Before(operation.StartedAt) {
				t.Fatalf("invalid failed projection: %+v", operation)
			}
			events := preparationEventsForTest(t, fixture.handler, fixture.ready)
			if len(events) != 7 || events[5].EventType != task.EventTypeReviewStarted || events[6].EventType != task.EventTypeReviewFailed || events[6].Payload.Review == nil || events[6].Payload.Review.ExecutionID != operation.ID || events[6].Payload.Review.ErrorCode != scenario.code || events[6].Payload.Artifact != nil || events[6].CausationID != "req-failure" {
				t.Fatalf("unexpected failure events: %+v", events)
			}
			if scenario.mode == "diff-unavailable" {
				if err := os.Rename(moved, fixture.ready.Path); err != nil {
					t.Fatal(err)
				}
			}
			fixture.setMode(t, "success")
			count := len(fixture.captures(t))
			replay := fixture.post(context.Background(), "failure-key", "req-failure-replay", fixture.ready.Version)
			assertReviewError(t, replay, scenario.status, scenario.code)
			if replay.Body.String() != failed.Body.String() || len(fixture.captures(t)) != count || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 7 {
				t.Fatal("failed key must replay its failure without new I/O or events")
			}
			retry := fixture.post(context.Background(), "explicit-new-attempt", "req-new-attempt", fixture.ready.Version)
			if retry.Code != http.StatusCreated {
				t.Fatalf("explicit new attempt: %d %s", retry.Code, retry.Body.String())
			}
			latest := fixture.latest(t)
			if latest.ID == operation.ID || latest.State != review.ExecutionSucceeded || latest.ErrorCode != "" || latest.Artifact == nil || len(fixture.captures(t)) != count+1 {
				t.Fatalf("invalid retry: %+v", latest)
			}
			if strings.Contains(logs.String(), "SECRET_MODEL_DIAGNOSTIC") || strings.Contains(failed.Body.String(), "SECRET_MODEL_DIAGNOSTIC") {
				t.Fatal("model diagnostics must not appear in public results or logs")
			}
		})
	}
}

func TestReviewExecutionCancellationAndTimeoutDoNotArchiveFindings(t *testing.T) {
	for _, scenario := range []struct {
		name, code string
		status     int
		timeout    time.Duration
		cancel     bool
	}{
		{"cancel", "review_canceled", http.StatusRequestTimeout, 5 * time.Second, true},
		{"timeout", "review_timeout", http.StatusGatewayTimeout, 500 * time.Millisecond, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newReviewExecutionFixture(t, "hang", scenario.timeout)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- fixture.post(ctx, "terminal-key", "req-terminal", fixture.ready.Version) }()
			fixture.waitStarted(t)
			if scenario.cancel {
				cancel()
			}
			select {
			case response := <-done:
				assertReviewError(t, response, scenario.status, scenario.code)
			case <-time.After(3 * time.Second):
				t.Fatal("terminal request did not finish")
			}
			operation := fixture.latest(t)
			wantState, wantEvent := review.ExecutionFailed, task.EventTypeReviewFailed
			if scenario.cancel {
				wantState, wantEvent = review.ExecutionCanceled, task.EventTypeReviewCanceled
			}
			if operation.State != wantState || operation.Artifact != nil || operation.ErrorCode != scenario.code || operation.CompletedAt.IsZero() {
				t.Fatalf("unexpected terminal projection: %+v", operation)
			}
			events := preparationEventsForTest(t, fixture.handler, fixture.ready)
			if len(events) != 7 || events[6].EventType != wantEvent || events[6].Payload.Review == nil || events[6].Payload.Review.ArtifactID != "" {
				t.Fatalf("unexpected terminal events: %+v", events)
			}
			fixture.setMode(t, "success")
			assertReviewError(t, fixture.post(context.Background(), "terminal-key", "req-replay-terminal", fixture.ready.Version), scenario.status, scenario.code)
			if len(fixture.captures(t)) != 1 {
				t.Fatal("terminal replay must not run again")
			}
			if retry := fixture.post(context.Background(), "new-key", "req-new-key", fixture.ready.Version); retry.Code != http.StatusCreated {
				t.Fatalf("retry after terminal result: %d %s", retry.Code, retry.Body.String())
			}
		})
	}
}

func TestReviewExecutionAcceptsAnEmptyFindingsReport(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "empty", 5*time.Second)
	response := fixture.post(context.Background(), "empty-findings", "req-empty", fixture.ready.Version)
	if response.Code != http.StatusCreated {
		t.Fatalf("empty Findings: %d %s", response.Code, response.Body.String())
	}
	operation := fixture.latest(t)
	stored := httptest.NewRecorder()
	fixture.handler.ServeHTTP(stored, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+operation.Artifact.ID+"/content?tenantId="+fixture.ready.TenantID, nil))
	report, err := review.ParseFindings(stored.Body.Bytes(), fixture.ready.BaseSHA, fixture.ready.HeadSHA)
	if stored.Code != http.StatusOK || err != nil || report.Findings == nil || len(report.Findings) != 0 {
		t.Fatalf("empty report must remain a valid Artifact: %s %v", stored.Body.String(), err)
	}
}

func TestReviewExecutionChecksAdmissionBeforeIO(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	assertReviewError(t, fixture.post(context.Background(), "stale", "req-stale", fixture.ready.Version-1), http.StatusConflict, "version_conflict")
	otherTenant := fixture
	otherTenant.ready.TenantID = "other-tenant"
	assertReviewError(t, otherTenant.post(context.Background(), "foreign", "req-foreign", fixture.ready.Version), http.StatusNotFound, "not_found")
	missing := fixture
	missing.ready.TaskID = "missing-task"
	assertReviewError(t, missing.post(context.Background(), "missing", "req-missing", fixture.ready.Version), http.StatusNotFound, "not_found")
	bugFix := fixture.createTask(t, fixture.ready.TenantID, "bug-fix", "BUG_FIX")
	other := fixture
	other.ready.TaskID = bugFix.ID
	assertReviewError(t, other.post(context.Background(), "bug-fix-review", "req-bug-fix", fixture.ready.Version), http.StatusConflict, "invalid_task_type")
	unqueued := fixture.createTask(t, fixture.ready.TenantID, "unqueued", "PR_REVIEW")
	other.ready.TaskID = unqueued.ID
	assertReviewError(t, other.post(context.Background(), "unqueued-review", "req-unqueued", fixture.ready.Version), http.StatusConflict, "task_not_queued")
	fixture.queueTask(t, unqueued)
	assertReviewError(t, other.post(context.Background(), "no-workspace", "req-no-workspace", fixture.ready.Version), http.StatusNotFound, "not_found")
	registered := fixture.registerTask(t, unqueued, "unqueued")
	other.ready = registered
	assertReviewError(t, other.post(context.Background(), "not-ready", "req-not-ready", registered.Version), http.StatusConflict, "workspace_not_ready")
	for _, body := range []string{`null`, `{}`, fixture.body("key", "req", 0), fixture.body(strings.Repeat("x", 257), "req", fixture.ready.Version), fixture.body("key", " ", fixture.ready.Version)} {
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+fixture.ready.TaskID+"/review", strings.NewReader(body)))
		assertReviewError(t, response, http.StatusBadRequest, "validation_error")
	}
	for _, body := range []string{`[]`, fixture.body("key", "req", fixture.ready.Version) + ` {}`, fixture.body("key", "req", fixture.ready.Version) + strings.Repeat(" ", 64<<10)} {
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+fixture.ready.TaskID+"/review", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed or oversized input: %d %s", response.Code, response.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertReviewError(t, fixture.post(ctx, "pre-canceled", "req-pre-canceled", fixture.ready.Version), http.StatusRequestTimeout, "review_canceled")
	if len(fixture.captures(t)) != 0 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 5 {
		t.Fatal("rejected admission must not execute or append review events")
	}
}

func TestReviewExecutionIsUnavailableInTheDefaultAssembly(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "unconfigured", 5*time.Second)
	if err := os.Rename(fixture.ready.Path, filepath.Join(t.TempDir(), "moved")); err != nil {
		t.Fatal(err)
	}
	assertReviewError(t, fixture.post(context.Background(), "disabled", "req-disabled", fixture.ready.Version), http.StatusServiceUnavailable, "review_unavailable")
	if len(fixture.captures(t)) != 0 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 5 {
		t.Fatal("disabled runtime must not claim an execution or perform I/O")
	}
	get := httptest.NewRecorder()
	fixture.handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+fixture.ready.TaskID+"/review?tenantId="+fixture.ready.TenantID, nil))
	assertReviewError(t, get, http.StatusNotFound, "not_found")
}

func TestReviewExecutionTenantKeysAndTaskBindingsAreIndependent(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	first := fixture.post(context.Background(), "shared-key", "req-owner", fixture.ready.Version)
	if first.Code != http.StatusCreated {
		t.Fatalf("first tenant: %d %s", first.Code, first.Body.String())
	}
	second := fixture
	second.ready = fixture.readyTask(t, fixture.ready.TenantID, "second-task")
	assertReviewError(t, second.post(context.Background(), "shared-key", "req-conflict", second.ready.Version), http.StatusConflict, "idempotency_conflict")
	foreign := fixture
	foreign.ready = fixture.readyTask(t, "tenant-b", "foreign-task")
	created := foreign.post(context.Background(), "shared-key", "req-foreign", foreign.ready.Version)
	if created.Code != http.StatusCreated {
		t.Fatalf("another tenant can use the same key: %d %s", created.Code, created.Body.String())
	}
	ownerResult, foreignResult := fixture.latest(t), foreign.latest(t)
	if ownerResult.ID == foreignResult.ID || ownerResult.Artifact.ID == foreignResult.Artifact.ID || foreignResult.Artifact.TenantID != "tenant-b" {
		t.Fatal("tenant keys must produce separate owned results")
	}
	if len(fixture.captures(t)) != 2 || len(preparationEventsForTest(t, fixture.handler, second.ready)) != 5 {
		t.Fatal("conflicting task binding must not execute")
	}
	for _, suffix := range []string{"", "/content"} {
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+foreignResult.Artifact.ID+suffix+"?tenantId="+fixture.ready.TenantID, nil))
		assertReviewError(t, response, http.StatusNotFound, "not_found")
	}
}

func TestReviewExecutionAllowsIndependentTasksToRunConcurrently(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "hang", 5*time.Second)
	second := fixture
	second.ready = fixture.readyTask(t, "tenant-b", "concurrent-other-task")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- fixture.post(ctx, "same-key", "req-first", fixture.ready.Version) }()
	go func() { done <- second.post(ctx, "same-key", "req-second", second.ready.Version) }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for len(fixture.captures(t)) < 2 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("independent tasks should both reach the external CLI")
		}
	}
	if fixture.latest(t).State != review.ExecutionRunning || second.latest(t).State != review.ExecutionRunning {
		t.Fatal("both tasks need independent RUNNING projections")
	}
	if err := os.WriteFile(fixture.release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		select {
		case response := <-done:
			if response.Code != http.StatusCreated {
				t.Fatalf("independent execution: %d %s", response.Code, response.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("independent task did not finish")
		}
	}
	for _, current := range []reviewExecutionFixture{fixture, second} {
		operation := current.latest(t)
		if operation.State != review.ExecutionSucceeded || operation.Artifact.TaskID != current.ready.TaskID || operation.Artifact.TenantID != current.ready.TenantID || len(preparationEventsForTest(t, current.handler, current.ready)) != 8 {
			t.Fatalf("independent task result: %+v", operation)
		}
	}
}

func TestReviewExecutionClaimsASimultaneousRequestOnlyOnce(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "hang", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const count = 12
	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, count)
	for index := 0; index < count; index++ {
		go func() {
			<-start
			done <- fixture.post(ctx, "simultaneous-key", fmt.Sprintf("req-simultaneous-%d", index), fixture.ready.Version)
		}()
	}
	close(start)
	fixture.waitStarted(t)
	if err := os.WriteFile(fixture.release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := 0
	var winner *httptest.ResponseRecorder
	var replays []*httptest.ResponseRecorder
	for index := 0; index < count; index++ {
		select {
		case response := <-done:
			switch response.Code {
			case http.StatusCreated:
				created++
				winner = response
			case http.StatusOK:
				replays = append(replays, response)
			case http.StatusConflict:
				assertReviewError(t, response, http.StatusConflict, "review_in_progress")
			default:
				t.Fatalf("simultaneous response: %d %s", response.Code, response.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("simultaneous requests did not finish")
		}
	}
	if created != 1 || len(fixture.captures(t)) != 1 {
		t.Fatalf("expected exactly one creation and CLI launch, got %d", created)
	}
	for _, replay := range replays {
		if replay.Body.String() != winner.Body.String() {
			t.Fatal("concurrent replay changed the winner result")
		}
	}
	events := preparationEventsForTest(t, fixture.handler, fixture.ready)
	if len(events) != 8 {
		t.Fatal("simultaneous requests must produce one review lifecycle")
	}
	for _, event := range events[5:] {
		if event.CausationID != winner.Header().Get("X-Request-ID") {
			t.Fatal("only the winning request may own execution events")
		}
	}
}

func (fixture reviewExecutionFixture) createTask(t *testing.T, tenantID, key, kind string) task.Task {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"type":%q,"goal":"Review feature","repository":{"provider":"gitlab","repositoryId":"platform/review","headSha":%q}}`, "req-create-"+key, "create-"+key, tenantID, kind, fixture.ready.HeadSHA))))
	if response.Code != http.StatusCreated {
		t.Fatalf("create additional Task: %d %s", response.Code, response.Body.String())
	}
	var current task.Task
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	return current
}

func (fixture reviewExecutionFixture) queueTask(t *testing.T, current task.Task) {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/"+current.ID, strings.NewReader(fmt.Sprintf(`{"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"expectedVersion":%d,"status":"QUEUED"}`, "req-queue-"+current.ID, "queue-"+current.ID, current.TenantID, current.Version))))
	if response.Code != http.StatusOK {
		t.Fatalf("queue additional Task: %d %s", response.Code, response.Body.String())
	}
}

func (fixture reviewExecutionFixture) registerTask(t *testing.T, current task.Task, key string) workspace.Workspace {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+current.ID+"/workspace", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"idempotencyKey":%q,"tenantId":%q}`, "req-register-"+key, "register-"+key, current.TenantID))))
	if response.Code != http.StatusCreated {
		t.Fatalf("register additional Workspace: %d %s", response.Code, response.Body.String())
	}
	var currentWorkspace workspace.Workspace
	if err := json.Unmarshal(response.Body.Bytes(), &currentWorkspace); err != nil {
		t.Fatal(err)
	}
	return currentWorkspace
}

func (fixture reviewExecutionFixture) readyTask(t *testing.T, tenantID, key string) workspace.Workspace {
	t.Helper()
	current := fixture.createTask(t, tenantID, key, "PR_REVIEW")
	fixture.queueTask(t, current)
	registered := fixture.registerTask(t, current, key)
	response := postWorkspacePreparationForTest(fixture.handler, context.Background(), registered, "prepare-"+key, "req-prepare-"+key, registered.Version)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare additional Workspace: %d %s", response.Code, response.Body.String())
	}
	var ready workspace.Workspace
	if err := json.Unmarshal(response.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	return ready
}

func (fixture reviewExecutionFixture) latest(t *testing.T) review.Execution {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+fixture.ready.TaskID+"/review?tenantId="+fixture.ready.TenantID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET execution: %d %s", response.Code, response.Body.String())
	}
	var operation review.Execution
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	return operation
}

func (fixture reviewExecutionFixture) waitStarted(t *testing.T) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if len(fixture.captures(t)) > 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("CLI did not start")
		}
	}
}

func (fixture reviewExecutionFixture) post(ctx context.Context, key, requestID string, version uint64) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+fixture.ready.TaskID+"/review", strings.NewReader(fixture.body(key, requestID, version))).WithContext(ctx))
	return response
}

func assertReviewError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || body.Error != code {
		t.Fatalf("expected %d %s, got %d %s", status, code, response.Code, response.Body.String())
	}
}

type reviewExecutionCapture struct {
	CWD   string `json:"cwd"`
	Input struct {
		BaseSHA string `json:"baseSha"`
		HeadSHA string `json:"headSha"`
		Patch   string `json:"patch"`
	} `json:"input"`
}

type reviewExecutionFixture struct {
	handler                     http.Handler
	ready                       workspace.Workspace
	config, captureDir, release string
}

func newReviewExecutionFixture(t *testing.T, mode string, timeout time.Duration) reviewExecutionFixture {
	t.Helper()
	source := t.TempDir()
	gitForReviewInputTest(t, source, "init", "--initial-branch=master")
	gitForReviewInputTest(t, source, "config", "user.name", "Agent Platform Test")
	gitForReviewInputTest(t, source, "config", "user.email", "agent-platform@example.invalid")
	writeReviewInputFixture(t, source, "README.md", "shared base\n")
	gitForReviewInputTest(t, source, "add", "README.md")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	gitForReviewInputTest(t, source, "checkout", "-b", "feature")
	writeReviewInputFixture(t, source, "feature.txt", "feature-only change\n")
	gitForReviewInputTest(t, source, "add", "feature.txt")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "feature")
	head := gitForReviewInputTest(t, source, "rev-parse", "HEAD")
	root := t.TempDir()
	preparer, err := gitworkspace.NewPreparer("git", root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gitworkspace.NewDiffReader("git", root)
	if err != nil {
		t.Fatal(err)
	}
	cliDir := t.TempDir()
	fixture := reviewExecutionFixture{config: filepath.Join(cliDir, "config.json"), captureDir: filepath.Join(cliDir, "captures"), release: filepath.Join(cliDir, "release")}
	if err := os.Mkdir(fixture.captureDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.setMode(t, mode)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\n" + `import json, os, sys, time, uuid
args = sys.argv[1:]
if args == ["--version"]:
    print("codex-cli 0.160.0")
elif args == ["exec", "--help"]:
    print("Options:\n  -s, --sandbox <MODE>\n          [possible values: read-only,workspace-write]\n      --ephemeral\n      --output-schema <FILE>\n  -o, --output-last-message <FILE>\n      --json\n      --ignore-user-config\n      --ignore-rules")
elif args == ["--help"]:
    print("Options:\n      --no-daemon\n  -a, --ask-for-approval <POLICY>\n  -m, --model <MODEL>")
else:
    with open(` + string(quoted) + `) as source:
        config = json.load(source)
    request = json.load(sys.stdin)
    capture = dict(cwd=os.getcwd(), input=request)
    capture_path = os.path.join(config["captureDir"], str(uuid.uuid4()) + ".json")
    with open(capture_path + ".tmp", "w") as target:
        json.dump(capture, target)
    os.replace(capture_path + ".tmp", capture_path)
    if config["mode"] == "hang":
        while not os.path.exists(config["release"]):
            time.sleep(0.02)
    if config["mode"] == "nonzero":
        print("SECRET_MODEL_DIAGNOSTIC", file=sys.stderr)
        sys.exit(23)
    finding = dict(title="Unchecked error", description="An error can be ignored.", severity="MEDIUM", confidence=0.8, path="feature.txt", startLine=1, endLine=1)
    report = dict(schemaVersion="1.0", baseSha=request["baseSha"], headSha=request["headSha"], findings=[finding, finding])
    if config["mode"] == "empty":
        report["findings"] = []
    if config["mode"] == "invalid":
        finding["severity"] = "INVALID"
    if config["mode"] == "wrong-head":
        report["headSha"] = "0" * 40
    with open(args[args.index("--output-last-message") + 1], "w") as target:
        json.dump(report, target)
    print('{"type":"turn.completed"}')
`
	binary := filepath.Join(cliDir, "codex-fixture")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if mode == "hang-diff" {
		gitBinary := filepath.Join(cliDir, "git-fixture")
		gitScript := "#!" + python + "\n" + `import sys, time
with open(` + string(quoted) + ` + ".git-started", "w") as target:
    target.write("started")
while True:
    time.sleep(0.02)
`
		if err := os.WriteFile(gitBinary, []byte(gitScript), 0o700); err != nil {
			t.Fatal(err)
		}
		reader, err = gitworkspace.NewDiffReader(gitBinary, root)
		if err != nil {
			t.Fatal(err)
		}
	}
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{Binary: binary, WorkspaceRoot: root, Model: "review-fixture", Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	prepare := workspacePreparerFunc(func(ctx context.Context, reference task.RepositoryReference, destination string) error {
		return preparer.Prepare(ctx, gitworkspace.Input{CloneURL: (&url.URL{Scheme: "file", Path: source}).String(), BaseSHA: reference.BaseSHA, HeadSHA: reference.HeadSHA, Destination: destination})
	})
	if mode == "unconfigured" {
		fixture.handler, err = NewHandlerWithWorkspaceServices(localReviewInputReferences{source: source}, prepare, reader, root, "")
	} else {
		fixture.handler, err = NewHandlerWithReviewServices(localReviewInputReferences{source: source}, prepare, reader, runner, root, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	created := httptest.NewRecorder()
	fixture.handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(fmt.Sprintf(`{"requestId":"create","idempotencyKey":"create","tenantId":"tenant-review","type":"PR_REVIEW","goal":"Review feature","repository":{"provider":"gitlab","repositoryId":"platform/review","headSha":%q}}`, head))))
	if created.Code != http.StatusCreated {
		t.Fatalf("create Task: %d %s", created.Code, created.Body.String())
	}
	queued := httptest.NewRecorder()
	fixture.handler.ServeHTTP(queued, httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task-1", strings.NewReader(`{"requestId":"queue","idempotencyKey":"queue","tenantId":"tenant-review","expectedVersion":1,"status":"QUEUED"}`)))
	if queued.Code != http.StatusOK {
		t.Fatalf("queue Task: %d %s", queued.Code, queued.Body.String())
	}
	registered := httptest.NewRecorder()
	fixture.handler.ServeHTTP(registered, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/workspace", strings.NewReader(`{"requestId":"register","idempotencyKey":"register","tenantId":"tenant-review"}`)))
	if registered.Code != http.StatusCreated {
		t.Fatalf("register Workspace: %d %s", registered.Code, registered.Body.String())
	}
	var current workspace.Workspace
	if err := json.Unmarshal(registered.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	prepared := postWorkspacePreparationForTest(fixture.handler, context.Background(), current, "prepare", "req-prepare", current.Version)
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare Workspace: %d %s", prepared.Code, prepared.Body.String())
	}
	if err := json.Unmarshal(prepared.Body.Bytes(), &fixture.ready); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture reviewExecutionFixture) setMode(t *testing.T, mode string) {
	t.Helper()
	content, err := json.Marshal(map[string]string{"mode": mode, "captureDir": fixture.captureDir, "release": fixture.release})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.config+".tmp", content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.config+".tmp", fixture.config); err != nil {
		t.Fatal(err)
	}
}

func (fixture reviewExecutionFixture) captures(t *testing.T) []reviewExecutionCapture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixture.captureDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]reviewExecutionCapture, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var capture reviewExecutionCapture
		if err := json.Unmarshal(content, &capture); err != nil {
			t.Fatal(err)
		}
		result = append(result, capture)
	}
	return result
}

func (fixture reviewExecutionFixture) body(key, requestID string, version uint64) string {
	return fmt.Sprintf(`{"requestId":%q,"idempotencyKey":%q,"tenantId":%q,"expectedWorkspaceVersion":%d}`, requestID, key, fixture.ready.TenantID, version)
}
