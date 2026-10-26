package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
)

func TestReviewHistoryReturnsTheOriginalExecutionAfterANewAttempt(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	first := fixture.post(context.Background(), "first-attempt", "req-first", fixture.ready.Version)
	if first.Code != http.StatusCreated {
		t.Fatal("first attempt failed")
	}
	original := fixture.latest(t)
	if next := fixture.post(context.Background(), "second-attempt", "req-second", fixture.ready.Version); next.Code != http.StatusCreated {
		t.Fatal("second attempt failed")
	}
	latest := fixture.latest(t)
	if latest.ID == original.ID {
		t.Fatal("new attempt did not create a new execution")
	}
	// Reading saved records must not depend on a still-readable worktree.
	if err := os.Rename(fixture.ready.Path, filepath.Join(t.TempDir(), "moved-worktree")); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedHandler(t, fixture.handler, fixture.ready.TenantID)
	address := "/api/v1/tasks/" + fixture.ready.TaskID + "/reviews/" + original.ID
	response := authRequest(t, handler, "GET", address+"?tenantId="+fixture.ready.TenantID, "", authTokenA, 200)
	var historical review.Execution
	if err := json.Unmarshal(response.Body.Bytes(), &historical); err != nil || historical.ID != original.ID || historical.Artifact == nil || historical.Artifact.ID != original.Artifact.ID {
		t.Fatal("historical lookup returned a different execution or artifact")
	}
	if response.Body.String() != first.Body.String() {
		t.Fatal("historical execution changed after a new attempt")
	}
	replay := fixture.post(context.Background(), "first-attempt", "req-replay", fixture.ready.Version)
	if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
		t.Fatal("historical lookup and idempotent replay disagree")
	}
	if fixture.latest(t).ID != latest.ID || len(fixture.captures(t)) != 2 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 11 {
		t.Fatal("historical read executed work or changed the latest projection")
	}
}

func TestReviewHistoryBindsAuthenticationTenantAndTask(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	if result := fixture.post(context.Background(), "owner-review", "req-owner", fixture.ready.Version); result.Code != http.StatusCreated {
		t.Fatal("owner review failed")
	}
	original := fixture.latest(t)
	other := fixture.createTask(t, fixture.ready.TenantID, "other-task", "PR_REVIEW")
	foreign := fixture.createTask(t, "tenant-b", "foreign-task", "PR_REVIEW")
	handler := authenticatedHandler(t, fixture.handler, fixture.ready.TenantID)
	address := "/api/v1/tasks/" + fixture.ready.TaskID + "/reviews/" + original.ID
	authRequest(t, handler, "GET", address, "", "", http.StatusUnauthorized)
	authRequest(t, handler, "GET", address+"?tenantId="+fixture.ready.TenantID, "", authTokenB, http.StatusForbidden)
	authRequest(t, handler, "GET", address+"?tenantId="+fixture.ready.TenantID+"&tenantId=tenant-b", "", authTokenA, http.StatusBadRequest)
	ownerQuery := "?tenantId=" + fixture.ready.TenantID
	authRequest(t, handler, "GET", address, "", authTokenA, http.StatusBadRequest)
	missing := authRequest(t, handler, "GET", "/api/v1/tasks/"+fixture.ready.TaskID+"/reviews/missing"+ownerQuery, "", authTokenA, http.StatusNotFound)
	for _, test := range []struct{ path, token string }{
		{address + "?tenantId=tenant-b", authTokenB},
		{"/api/v1/tasks/" + other.ID + "/reviews/" + original.ID + ownerQuery, authTokenA},
		{"/api/v1/tasks/" + foreign.ID + "/reviews/" + original.ID + "?tenantId=tenant-b", authTokenB},
		{"/api/v1/tasks/missing/reviews/" + original.ID + ownerQuery, authTokenA},
	} {
		response := authRequest(t, handler, "GET", test.path, "", test.token, http.StatusNotFound)
		if response.Body.String() != missing.Body.String() {
			t.Fatal("inaccessible records must not reveal their existence")
		}
	}
	authRequest(t, handler, "GET", address+ownerQuery, "", authTokenA, http.StatusOK)
	if len(fixture.captures(t)) != 1 || len(preparationEventsForTest(t, fixture.handler, fixture.ready)) != 8 {
		t.Fatal("lookup must not execute work or append events")
	}
}

func TestReviewHistoryPreservesAFailedAttemptAfterRecovery(t *testing.T) {
	fixture := newReviewExecutionFixture(t, "invalid", 5*time.Second)
	assertReviewError(t, fixture.post(context.Background(), "invalid", "req-invalid", fixture.ready.Version), http.StatusBadGateway, "review_findings_invalid")
	failed := fixture.latest(t)
	handler := authenticatedHandler(t, fixture.handler, fixture.ready.TenantID)
	address := "/api/v1/tasks/" + fixture.ready.TaskID + "/reviews/" + failed.ID + "?tenantId=" + fixture.ready.TenantID
	before := authRequest(t, handler, "GET", address, "", authTokenA, http.StatusOK)
	fixture.setMode(t, "success")
	if result := fixture.post(context.Background(), "recovery", "req-recovery", fixture.ready.Version); result.Code != http.StatusCreated {
		t.Fatal("recovery failed")
	}
	after := authRequest(t, handler, "GET", address, "", authTokenA, http.StatusOK)
	if before.Body.String() != after.Body.String() || failed.State != review.ExecutionFailed || failed.Artifact != nil || failed.ErrorCode != "review_findings_invalid" {
		t.Fatal("failed historical record was replaced by the new success")
	}
}

func TestReviewHistoryFollowsAnExecutionFromRunningToTerminal(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "succeeded"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newReviewExecutionFixture(t, "hang", 5*time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- fixture.post(ctx, "running", "req-running", fixture.ready.Version) }()
			fixture.waitStarted(t)
			running := fixture.latest(t)
			handler := authenticatedHandler(t, fixture.handler, fixture.ready.TenantID)
			address := "/api/v1/tasks/" + fixture.ready.TaskID + "/reviews/" + running.ID + "?tenantId=" + fixture.ready.TenantID
			response := authRequest(t, handler, "GET", address, "", authTokenA, http.StatusOK)
			var observed review.Execution
			if err := json.Unmarshal(response.Body.Bytes(), &observed); err != nil || observed.State != review.ExecutionRunning || observed.Artifact != nil {
				t.Fatal("running execution unavailable through its ID")
			}
			if canceled {
				cancel()
			} else if err := os.WriteFile(fixture.release, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if canceled {
					assertReviewError(t, result, http.StatusRequestTimeout, "review_canceled")
				} else if result.Code != http.StatusCreated {
					t.Fatal("running execution did not succeed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("execution did not finish")
			}
			response = authRequest(t, handler, "GET", address, "", authTokenA, http.StatusOK)
			var terminal review.Execution
			if err := json.Unmarshal(response.Body.Bytes(), &terminal); err != nil || terminal.ID != running.ID || terminal.CompletedAt.IsZero() {
				t.Fatal("lookup did not reflect the terminal transition")
			}
			if canceled && (terminal.State != review.ExecutionCanceled || terminal.Artifact != nil || terminal.ErrorCode != "review_canceled") {
				t.Fatal("cancellation was not saved")
			}
			if !canceled && (terminal.State != review.ExecutionSucceeded || terminal.Artifact == nil) {
				t.Fatal("success was not saved")
			}
			if len(fixture.captures(t)) != 1 {
				t.Fatal("history lookup started another execution")
			}
		})
	}
}
