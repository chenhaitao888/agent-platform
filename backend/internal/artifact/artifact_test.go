package artifact

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
)

func TestStoreKeepsArtifactContentImmutable(t *testing.T) {
	store := NewStore()
	content := []byte("diff --git a/README.md b/README.md\n-base\n+head\n")
	expectedContent := string(content)
	expectedSHA := fmt.Sprintf("%x", sha256.Sum256(content))

	created, err := store.Create(CreateInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		WorkspaceID:    "workspace-1",
		IdempotencyKey: "archive-diff-1",
		Type:           TypeRepositoryDiff,
		MediaType:      "text/x-diff",
		Content:        content,
	})
	if err != nil {
		t.Fatalf("create Artifact: %v", err)
	}
	if !created.Created {
		t.Fatal("expected the first request to create an Artifact")
	}
	if created.Artifact.ID == "" || created.Artifact.SHA256 != expectedSHA {
		t.Fatalf("unexpected Artifact metadata: %#v", created.Artifact)
	}
	if created.Artifact.SizeBytes != int64(len(expectedContent)) {
		t.Fatalf("expected size %d, got %d", len(expectedContent), created.Artifact.SizeBytes)
	}

	content[0] = 'X'
	artifact, storedContent, ok := store.GetContent(created.Artifact.ID, "tenant-a")
	if !ok {
		t.Fatal("expected the Artifact to be readable by its tenant")
	}
	if artifact != created.Artifact || string(storedContent) != expectedContent {
		t.Fatalf("expected immutable stored content, got Artifact=%#v content=%q", artifact, storedContent)
	}
	if _, _, ok := store.GetContent(created.Artifact.ID, "tenant-b"); ok {
		t.Fatal("expected the Artifact to be hidden from another tenant")
	}

	storedContent[0] = 'Y'
	_, rereadContent, ok := store.GetContent(created.Artifact.ID, "tenant-a")
	if !ok || string(rereadContent) != expectedContent {
		t.Fatalf("expected reads to return a copy, got %q", rereadContent)
	}
}

func TestStoreReplaysTheSameIdempotentCreate(t *testing.T) {
	store := NewStore()
	input := CreateInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		WorkspaceID:    "workspace-1",
		IdempotencyKey: "archive-diff-1",
		Type:           TypeRepositoryDiff,
		MediaType:      "text/x-diff",
		Content:        []byte("immutable diff"),
	}

	first, err := store.Create(input)
	if err != nil {
		t.Fatalf("create first Artifact: %v", err)
	}
	replay, err := store.Create(input)
	if err != nil {
		t.Fatalf("replay Artifact creation: %v", err)
	}

	if !first.Created || replay.Created {
		t.Fatalf("expected create then replay, got first=%#v replay=%#v", first, replay)
	}
	if replay.Artifact != first.Artifact {
		t.Fatalf("expected replay of %#v, got %#v", first.Artifact, replay.Artifact)
	}
}

func TestStoreRejectsAnIdempotencyKeyWithDifferentContent(t *testing.T) {
	store := NewStore()
	input := CreateInput{
		TenantID:       "tenant-a",
		TaskID:         "task-1",
		WorkspaceID:    "workspace-1",
		IdempotencyKey: "archive-diff-1",
		Type:           TypeRepositoryDiff,
		MediaType:      "text/x-diff",
		Content:        []byte("first diff"),
	}
	if _, err := store.Create(input); err != nil {
		t.Fatalf("create first Artifact: %v", err)
	}

	input.Content = []byte("different diff")
	_, err := store.Create(input)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}
