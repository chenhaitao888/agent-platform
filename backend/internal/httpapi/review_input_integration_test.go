package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"agent-platform/backend/internal/workspace"
)

// This exercises real Git and HTTP. The only substituted boundary is GitLab:
// a local repository supplies immutable references and a file:// clone source.
func TestReviewInputRemainsFrozenThroughRealGitHTTPAndArtifact(t *testing.T) {
	source := t.TempDir()
	gitForReviewInputTest(t, source, "init", "--initial-branch=master")
	gitForReviewInputTest(t, source, "config", "user.name", "Agent Platform Test")
	gitForReviewInputTest(t, source, "config", "user.email", "agent-platform@example.invalid")
	writeReviewInputFixture(t, source, "README.md", "shared base\n")
	gitForReviewInputTest(t, source, "add", "README.md")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	baseSHA := gitForReviewInputTest(t, source, "rev-parse", "HEAD")
	writeReviewInputFixture(t, source, "target.txt", "target-only change\n")
	gitForReviewInputTest(t, source, "add", "target.txt")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "target")
	targetSHA := gitForReviewInputTest(t, source, "rev-parse", "HEAD")
	gitForReviewInputTest(t, source, "checkout", "-b", "feature", baseSHA)
	writeReviewInputFixture(t, source, "feature.txt", "feature-only change\n")
	gitForReviewInputTest(t, source, "add", "feature.txt")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "feature")
	headSHA := gitForReviewInputTest(t, source, "rev-parse", "HEAD")

	root := t.TempDir()
	preparer, err := gitworkspace.NewPreparer("git", root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gitworkspace.NewDiffReader("git", root)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithWorkspaceServices(
		localReviewInputReferences{source: source},
		workspacePreparerFunc(func(ctx context.Context, reference task.RepositoryReference, destination string) error {
			return preparer.Prepare(ctx, gitworkspace.Input{
				CloneURL: (&url.URL{Scheme: "file", Path: source}).String(),
				BaseSHA:  reference.BaseSHA, HeadSHA: reference.HeadSHA, Destination: destination,
			})
		}), reader, root, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 15 * time.Second
	createBody := fmt.Sprintf(`{
		"requestId":"req-input-create","idempotencyKey":"input-create","tenantId":"tenant-input",
		"type":"PR_REVIEW","goal":"Review only the feature changes",
		"repository":{"provider":"gitlab","repositoryId":"platform/input-fixture","headSha":%q}
	}`, headSHA)
	created, _ := requestReviewInputTest(t, client, server.URL+"/api/v1/tasks", http.MethodPost, createBody, http.StatusCreated)
	var currentTask task.Task
	if err := json.Unmarshal(created, &currentTask); err != nil {
		t.Fatal(err)
	}
	if currentTask.Repository.BaseSHA != baseSHA || currentTask.Repository.TargetSHA != targetSHA || currentTask.Repository.HeadSHA != headSHA {
		t.Fatalf("Task must freeze the merge-base/target/head: %+v", currentTask.Repository)
	}
	taskURL := server.URL + "/api/v1/tasks/" + currentTask.ID

	// Moving master after creation must affect neither replay nor the later clone/diff.
	gitForReviewInputTest(t, source, "checkout", "master")
	writeReviewInputFixture(t, source, "later.txt", "later master change\n")
	gitForReviewInputTest(t, source, "add", "later.txt")
	gitForReviewInputTest(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "advance master")
	replayed, _ := requestReviewInputTest(t, client, server.URL+"/api/v1/tasks", http.MethodPost, createBody, http.StatusOK)
	if string(replayed) != string(created) {
		t.Fatal("Task replay changed its frozen repository input")
	}
	requestReviewInputTest(t, client, taskURL, http.MethodPatch, `{
		"requestId":"req-input-queue","idempotencyKey":"input-queue","tenantId":"tenant-input",
		"expectedVersion":1,"status":"QUEUED"
	}`, http.StatusOK)
	requestReviewInputTest(t, client, taskURL+"/workspace", http.MethodPost, `{
		"requestId":"req-input-register","idempotencyKey":"input-register","tenantId":"tenant-input"
	}`, http.StatusCreated)
	prepared, _ := requestReviewInputTest(t, client, taskURL+"/workspace/prepare", http.MethodPost, `{
		"requestId":"req-input-prepare","idempotencyKey":"input-prepare","tenantId":"tenant-input","expectedVersion":1
	}`, http.StatusOK)
	var ready workspace.Workspace
	if err := json.Unmarshal(prepared, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.State != workspace.StateReady || ready.Version != 3 || ready.BaseSHA != baseSHA || ready.HeadSHA != headSHA {
		t.Fatalf("unexpected READY input: %+v", ready)
	}
	if gitForReviewInputTest(t, ready.Path, "rev-parse", "HEAD") != headSHA {
		t.Fatal("worktree HEAD differs from the fixed review head")
	}
	if _, err := os.Stat(filepath.Join(ready.Path, "target.txt")); !os.IsNotExist(err) {
		t.Fatalf("target-only file must not enter the feature worktree: %v", err)
	}

	diffBody, _ := requestReviewInputTest(t, client, taskURL+"/workspace/diff?tenantId=tenant-input", http.MethodGet, "", http.StatusOK)
	var diff review.Diff
	if err := json.Unmarshal(diffBody, &diff); err != nil {
		t.Fatal(err)
	}
	if diff.TaskID != currentTask.ID || diff.WorkspaceID != ready.ID || diff.BaseSHA != baseSHA || diff.HeadSHA != headSHA || diff.MediaType != "text/x-diff" {
		t.Fatalf("diff must retain the Task/Workspace coordinates: %+v", diff)
	}
	if !strings.Contains(diff.Patch, "+feature-only change") || strings.Contains(diff.Patch, "target.txt") || strings.Contains(diff.Patch, "later.txt") {
		t.Fatalf("review diff includes unrelated target changes: %s", diff.Patch)
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(diff.Patch)))
	if diff.SHA256 != wantHash || diff.SizeBytes != int64(len(diff.Patch)) {
		t.Fatal("diff hash/size does not describe its complete content")
	}
	archiveBody := `{
		"requestId":"req-input-archive","idempotencyKey":"input-archive","tenantId":"tenant-input","expectedWorkspaceVersion":3
	}`
	archived, location := requestReviewInputTest(t, client, taskURL+"/artifacts/diff", http.MethodPost, archiveBody, http.StatusCreated)
	var metadata artifact.Artifact
	if err := json.Unmarshal(archived, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.TaskID != currentTask.ID || metadata.WorkspaceID != ready.ID || metadata.SHA256 != wantHash || metadata.SizeBytes != diff.SizeBytes {
		t.Fatalf("Artifact must describe the same immutable diff: %+v", metadata)
	}
	content, headers := requestReviewInputTest(t, client, server.URL+location.Get("Location")+"/content?tenantId=tenant-input", http.MethodGet, "", http.StatusOK)
	if string(content) != diff.Patch || headers.Get("ETag") != `"`+wantHash+`"` || headers.Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Fatal("Artifact content/headers differ from the original fixed diff")
	}
	requestReviewInputTest(t, client, server.URL+location.Get("Location")+"/content?tenantId=other-tenant", http.MethodGet, "", http.StatusNotFound)
	rearchive, _ := requestReviewInputTest(t, client, taskURL+"/artifacts/diff", http.MethodPost, archiveBody, http.StatusOK)
	if string(rearchive) != string(archived) {
		t.Fatal("archive replay must return the original Artifact")
	}
	eventBody, _ := requestReviewInputTest(t, client, taskURL+"/events?tenantId=tenant-input", http.MethodGet, "", http.StatusOK)
	var events listTaskEventsResponse
	if err := json.Unmarshal(eventBody, &events); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"task.created", "task.queued", "workspace.registered", "workspace.preparing", "workspace.ready", "artifact.created"}
	if len(events.Items) != len(wantEvents) {
		t.Fatalf("expected one event per transition, got %d", len(events.Items))
	}
	for index, event := range events.Items {
		if string(event.EventType) != wantEvents[index] || event.Sequence != uint64(index+1) {
			t.Fatalf("unexpected event %d: %+v", index, event)
		}
	}
}

type localReviewInputReferences struct{ source string }

func (r localReviewInputReferences) Resolve(ctx context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
	target, err := exec.CommandContext(ctx, "git", "-C", r.source, "rev-parse", "refs/heads/master").Output()
	if err != nil {
		return task.RepositoryReference{}, err
	}
	selection.TargetBranch = "master"
	selection.TargetSHA = strings.TrimSpace(string(target))
	base, err := exec.CommandContext(ctx, "git", "-C", r.source, "merge-base", selection.TargetSHA, selection.HeadSHA).Output()
	selection.BaseSHA = strings.TrimSpace(string(base))
	return selection, err
}

func (r localReviewInputReferences) Verify(ctx context.Context, reference task.RepositoryReference) error {
	for _, revision := range []string{reference.BaseSHA, reference.HeadSHA} {
		if err := exec.CommandContext(ctx, "git", "-C", r.source, "cat-file", "-e", revision+"^{commit}").Run(); err != nil {
			return err
		}
	}
	return nil
}

func gitForReviewInputTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Git fixture %v failed: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeReviewInputFixture(t *testing.T, source, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requestReviewInputTest(t *testing.T, client *http.Client, address, method, body string, wantStatus int) ([]byte, http.Header) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: expected %d, got %d: %s", method, address, wantStatus, response.StatusCode, content)
	}
	return content, response.Header
}
