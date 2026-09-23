package task

import (
	"testing"
	"time"
)

func TestListEventsReturnsIndependentPayloadSnapshots(t *testing.T) {
	store := NewStore()
	created, err := store.Create(CreateInput{
		RequestID:      "req-create",
		TenantID:       "tenant-a",
		IdempotencyKey: "create-task",
		Type:           "PR_REVIEW",
		Goal:           "Review a change",
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	workspacePayload := &WorkspaceEventPayload{WorkspaceID: "workspace-1", State: "REGISTERED", Version: 1}
	store.AppendEvent(AppendEventInput{
		TenantID:    "tenant-a",
		TaskID:      created.Task.ID,
		EventType:   EventTypeWorkspaceRegistered,
		CausationID: "req-register",
		OccurredAt:  time.Now().UTC(),
		Payload:     EventPayload{Workspace: workspacePayload},
	})

	workspacePayload.State = "changed by caller"
	listed, ok := store.ListEvents(created.Task.ID, "tenant-a")
	if !ok || len(listed) != 2 {
		t.Fatalf("expected two Task events, got %#v", listed)
	}
	if listed[1].Payload.Workspace.State != "REGISTERED" {
		t.Fatalf("input payload mutation changed stored event: %#v", listed[1].Payload)
	}
	listed[0].Payload.Task.Status = "changed by reader"
	listed[1].Payload.Workspace.State = "changed by reader"
	reread, ok := store.ListEvents(created.Task.ID, "tenant-a")
	if !ok || reread[0].Payload.Task.Status != StatusCreated || reread[1].Payload.Workspace.State != "REGISTERED" {
		t.Fatalf("returned payload mutation changed stored events: %#v", reread)
	}
}
