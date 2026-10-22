package task_test

import (
	"testing"

	"agent-platform/backend/internal/task"
)

func TestEventAppendCannotPolluteAnotherTenantsTimeline(t *testing.T) {
	store := task.NewStore()
	created, err := store.Create(task.CreateInput{TenantID: "tenant-a", IdempotencyKey: "create", RequestID: "create", Type: "PR_REVIEW", Goal: "review"})
	if err != nil {
		t.Fatal(err)
	}
	store.AppendEvent(task.AppendEventInput{TenantID: "tenant-b", TaskID: created.Task.ID, EventType: task.EventTypeTaskQueued})
	store.AppendEvent(task.AppendEventInput{TenantID: "tenant-a", TaskID: "missing-task", EventType: task.EventTypeTaskQueued})
	events, ok := store.ListEvents(created.Task.ID, "tenant-a")
	if !ok || len(events) != 1 || events[0].TenantID != "tenant-a" {
		t.Fatal("foreign event polluted timeline")
	}
	if _, ok := store.ListEvents(created.Task.ID, "tenant-b"); ok {
		t.Fatal("foreign tenant read events")
	}
	store.AppendEvent(task.AppendEventInput{TenantID: "tenant-a", TaskID: created.Task.ID, EventType: task.EventTypeTaskQueued})
	events, ok = store.ListEvents(created.Task.ID, "tenant-a")
	if !ok || len(events) != 2 || events[1].Sequence != 2 || events[1].EventID != "evt-2" {
		t.Fatal("rejected append changed event sequence")
	}
}
