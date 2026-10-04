package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListTaskEventsReturnsTheCreationEvent(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-event",
			"idempotencyKey":"create-event",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d", http.StatusCreated, createResponse.Code)
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("expected events status %d, got %d", http.StatusOK, eventsResponse.Code)
	}

	var body struct {
		Items []struct {
			SchemaVersion string `json:"schemaVersion"`
			EventID       string `json:"eventId"`
			EventType     string `json:"eventType"`
			OccurredAt    string `json:"occurredAt"`
			TenantID      string `json:"tenantId"`
			TaskID        string `json:"taskId"`
			Sequence      uint64 `json:"sequence"`
			CorrelationID string `json:"correlationId"`
			CausationID   string `json:"causationId"`
			Payload       struct {
				Task struct {
					Status  string `json:"status"`
					Version uint64 `json:"version"`
				} `json:"task"`
			} `json:"payload"`
		} `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 event, got %d", len(body.Items))
	}

	created := body.Items[0]
	if created.SchemaVersion != "2.0" || created.EventID != "evt-1" || created.EventType != "task.created" {
		t.Errorf("unexpected event identity: %#v", created)
	}
	if created.OccurredAt == "" {
		t.Error("expected occurredAt to be present")
	}
	if created.TenantID != "tenant-a" || created.TaskID != "task-1" || created.Sequence != 1 {
		t.Errorf("unexpected event scope: %#v", created)
	}
	if created.CorrelationID != "task-1" || created.CausationID != "req-create-event" {
		t.Errorf("unexpected event tracing fields: %#v", created)
	}
	if created.Payload.Task.Status != "CREATED" || created.Payload.Task.Version != 1 {
		t.Errorf("unexpected event payload: %#v", created.Payload)
	}
}

func TestListTaskEventsReturnsTheQueuedEventAfterCreation(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-before-queue-event",
			"idempotencyKey":"create-before-queue-event",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	updateRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/tasks/task-1",
		strings.NewReader(`{
			"requestId":"req-queue-event",
			"idempotencyKey":"queue-event",
			"tenantId":"tenant-a",
			"expectedVersion":1,
			"status":"QUEUED"
		}`),
	)
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("expected update status %d, got %d", http.StatusOK, updateResponse.Code)
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	var body struct {
		Items []struct {
			EventType   string `json:"eventType"`
			Sequence    uint64 `json:"sequence"`
			CausationID string `json:"causationId"`
			Payload     struct {
				Task struct {
					Status  string `json:"status"`
					Version uint64 `json:"version"`
				} `json:"task"`
			} `json:"payload"`
		} `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 events, got %d", len(body.Items))
	}

	created, queued := body.Items[0], body.Items[1]
	if created.EventType != "task.created" || created.Sequence != 1 {
		t.Errorf("unexpected creation event: %#v", created)
	}
	if queued.EventType != "task.queued" || queued.Sequence != 2 {
		t.Errorf("unexpected queued event: %#v", queued)
	}
	if queued.CausationID != "req-queue-event" {
		t.Errorf("expected queue request as causation, got %q", queued.CausationID)
	}
	if queued.Payload.Task.Status != "QUEUED" || queued.Payload.Task.Version != 2 {
		t.Errorf("unexpected queued payload: %#v", queued.Payload)
	}
}

func TestListTaskEventsDoesNotDuplicateAnIdempotentTransition(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-before-event-replay",
			"idempotencyKey":"create-before-event-replay",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	for _, requestID := range []string{"req-queue-event-first", "req-queue-event-replay"} {
		updateRequest := httptest.NewRequest(
			http.MethodPatch,
			"/api/v1/tasks/task-1",
			strings.NewReader(`{
				"requestId":"`+requestID+`",
				"idempotencyKey":"queue-event-replay",
				"tenantId":"tenant-a",
				"expectedVersion":1,
				"status":"QUEUED"
			}`),
		)
		updateResponse := httptest.NewRecorder()
		handler.ServeHTTP(updateResponse, updateRequest)
		if updateResponse.Code != http.StatusOK {
			t.Fatalf("expected update status %d, got %d", http.StatusOK, updateResponse.Code)
		}
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-a",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected creation and queued events only, got %d events", len(body.Items))
	}
}

func TestListTaskEventsHidesTasksFromOtherTenants(t *testing.T) {
	handler := newHandlerWithVerifiedRepositories()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks",
		strings.NewReader(`{
			"requestId":"req-create-private-events",
			"idempotencyKey":"create-private-events",
			"tenantId":"tenant-a",
			"type":"PR_REVIEW",
			"goal":"Review pull request 42",
			`+testRepositoryJSON+`
		}`),
	)
	handler.ServeHTTP(httptest.NewRecorder(), createRequest)

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events?tenantId=tenant-b",
		nil,
	)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)

	if eventsResponse.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, eventsResponse.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(eventsResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode events error response: %v", err)
	}
	if body.Error != "not_found" || body.Message != "task not found" {
		t.Errorf("unexpected error response: %#v", body)
	}
}

func TestListTaskEventsRequiresTenantID(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/events",
		nil,
	)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode events error response: %v", err)
	}
	if body.Error != "validation_error" || body.Message != "tenantId is required" {
		t.Errorf("unexpected error response: %#v", body)
	}
}
