package task

import "testing"

func TestCreateReplayKeepsFirstResolvedMasterSnapshot(t *testing.T) {
	store := NewStore()
	input := CreateInput{
		RequestID:      "req-1",
		TenantID:       "tenant-a",
		IdempotencyKey: "review-1",
		Type:           "PR_REVIEW",
		Goal:           "Review feature",
		Repository: RepositoryReference{
			Provider:     "gitlab",
			RepositoryID: "platform/project",
			TargetBranch: "master",
			TargetSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BaseSHA:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			HeadSHA:      "cccccccccccccccccccccccccccccccccccccccc",
		},
	}
	first, err := store.Create(input)
	if err != nil || !first.Created {
		t.Fatalf("first creation failed: result=%#v err=%v", first, err)
	}

	// 模拟并发请求各自看到不同的 master；赢家的快照不能被后来者覆盖。
	input.Repository.TargetSHA = "dddddddddddddddddddddddddddddddddddddddd"
	input.Repository.BaseSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	replay, err := store.Create(input)
	if err != nil || replay.Created || replay.Task != first.Task {
		t.Fatalf("same caller input must replay first snapshot: first=%#v replay=%#v err=%v", first, replay, err)
	}
	if events, ok := store.ListEvents(first.Task.ID, input.TenantID); !ok || len(events) != 1 {
		t.Fatalf("replay must not append another creation event, got %#v", events)
	}
}
