package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/task"
)

// Only external ports are faked: the HTTP router, task admission, workspace,
// execution and artifact stores are the same instances used in deployment.
type deliveryFixture struct {
	mu                      sync.Mutex
	branches, legacy, runs  int
	verified                []task.RepositoryReference
	failHead                string
	failVerify, failPrepare string
	run                     func(context.Context, review.RunInput) error
}

func (f *deliveryFixture) ResolveBranches(_ context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches++
	if in.SourceBranch == "missing" {
		return task.RepositoryReference{}, repository.ErrSourceBranchNotFound
	}
	return task.RepositoryReference{Provider: "gitlab", RepositoryID: in.RepositoryID, TargetBranch: in.TargetBranch,
		HeadSHA: strings.Repeat(in.RepositoryID, 40), BaseSHA: strings.Repeat("a", 40), TargetSHA: strings.Repeat("b", 40)}, nil
}
func (f *deliveryFixture) Resolve(context.Context, task.RepositoryReference) (task.RepositoryReference, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.legacy++
	return task.RepositoryReference{}, repository.ErrVerificationUnavailable
}
func (f *deliveryFixture) Verify(_ context.Context, ref task.RepositoryReference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, ref)
	if ref.RepositoryID == f.failVerify {
		return repository.ErrHeadCommitNotFound
	}
	return nil
}
func (f *deliveryFixture) Prepare(_ context.Context, ref task.RepositoryReference, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ref.RepositoryID == f.failPrepare {
		return errors.New("private downstream details")
	}
	return nil
}
func (*deliveryFixture) Read(context.Context, repository.DiffInput) ([]byte, error) {
	return []byte("diff --git a/a.go b/a.go\n"), nil
}
func (f *deliveryFixture) Run(ctx context.Context, in review.RunInput) (review.FindingsReport, error) {
	f.mu.Lock()
	f.runs++
	fail, run := f.failHead, f.run
	f.mu.Unlock()
	if in.HeadSHA == fail {
		return review.FindingsReport{}, review.ErrInvalidFindings
	}
	if run != nil {
		if err := run(ctx, in); err != nil {
			return review.FindingsReport{}, err
		}
	}
	return review.FindingsReport{SchemaVersion: "1.0", BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, Findings: []review.Finding{}}, nil
}
func deliveryHandler(t *testing.T, f *deliveryFixture, enabled bool) http.Handler {
	t.Helper()
	credentials := []Credential{}
	for _, subject := range []string{"alice", "reader", "outsider", "bob"} {
		token, tenant := subject+"-token-at-least-32-characters-long", "tenant-a"
		if subject == "alice" {
			token = changeToken
		}
		if subject == "bob" {
			tenant = "tenant-b"
		}
		credentials = append(credentials, Credential{Subject: subject, TenantID: tenant, TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), RepositoryIDs: []string{"1", "2", "3"}})
	}
	auth, err := NewAuthenticator(credentials)
	if err != nil {
		t.Fatal(err)
	}
	config := servicecatalog.Config{Services: []servicecatalog.Entry{}, Grants: []servicecatalog.Grant{}}
	for i, target := range []string{"main", "release/test", "master"} {
		id := fmt.Sprintf("S%d", i+1)
		config.Services = append(config.Services, servicecatalog.Entry{TenantID: "tenant-a", ServiceID: id, Name: id, CatalogVersion: "1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: fmt.Sprint(i + 1), DefaultTargetBranch: target, AllowedTargetBranches: []string{target}}, Environments: []string{"test"}})
		for _, subject := range []string{"alice", "reader"} {
			ops := []string{"read"}
			if subject == "alice" {
				ops = []string{"read", "create", "snapshot", "review"}
			}
			config.Grants = append(config.Grants, servicecatalog.Grant{Subject: subject, TenantID: "tenant-a", ServiceID: id, Environment: "test", Operations: ops})
		}
	}
	catalog, err := servicecatalog.New(config, auth)
	if err != nil {
		t.Fatal(err)
	}
	var runner review.Runner
	if enabled {
		runner = f
	}
	h, err := NewHandlerWithDeliveryServices(f, f, f, runner, catalog, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return auth.Wrap(h)
}

const deliverySelections = `[{"serviceId":"S1","sourceBranch":"feature/A"},{"serviceId":"S2","sourceBranch":"feature/B"},{"serviceId":"S3","sourceBranch":"feature/C"}]`

func createDeliverySnapshot(t *testing.T, h http.Handler) change.SnapshotResult {
	t.Helper()
	changeRequest(t, h, "POST", "/api/v1/changes", `{"idempotencyKey":"create","title":"需求 A","environment":"test","services":`+deliverySelections+`}`, 201)
	var result change.SnapshotResult
	body := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"freeze","expectedVersion":1,"services":`+deliverySelections+`}`, 201)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestChangeReviewUsesFrozenServicesAndSharedWorkbench(t *testing.T) {
	f := &deliveryFixture{}
	h := deliveryHandler(t, f, true)
	snapshot := createDeliverySnapshot(t, h)
	request := `{"idempotencyKey":"review-1","expectedVersion":2,"snapshotId":"snapshot-1"}`
	body := changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", request, 201)
	var result change.ReviewResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Review.State != "SUCCEEDED" || len(result.Review.Services) != 3 {
		t.Fatalf("incomplete review: %s", body)
	}
	for i, item := range result.Review.Services {
		if item.State != "SUCCEEDED" || item.ExecutionID == "" || item.ArtifactID == "" {
			t.Fatalf("missing evidence: %+v", item)
		}
		var child task.Task
		if err := json.Unmarshal(changeRequest(t, h, "GET", "/api/v1/tasks/"+item.TaskID+"?tenantId=tenant-a", "", 200), &child); err != nil {
			t.Fatal(err)
		}
		if child.Status != task.StatusQueued || child.Repository != *snapshot.Snapshot.Services[i].Repository {
			t.Fatalf("coordinates drifted: %+v", child)
		}
		changeRequest(t, h, "GET", "/api/v1/tasks/"+item.TaskID+"/workspace/diff?tenantId=tenant-a", "", 200)
		changeRequest(t, h, "GET", "/api/v1/tasks/"+item.TaskID+"/reviews/"+item.ExecutionID+"?tenantId=tenant-a", "", 200)
		changeRequest(t, h, "GET", "/api/v1/artifacts/"+item.ArtifactID+"/content?tenantId=tenant-a", "", 200)
	}
	replayed := changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", request, 200)
	if string(replayed) != string(body) || f.runs != 3 || f.branches != 3 || f.legacy != 0 || len(f.verified) != 3 {
		t.Fatal("replay reran work or bypassed frozen admission")
	}
}

func readDeliveryResult(t *testing.T, body []byte) change.ReviewResult {
	t.Helper()
	var result change.ReviewResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestChangeReviewRetriesOnlyFailedServicesAndPreservesSnapshotHistory(t *testing.T) {
	f := &deliveryFixture{failHead: strings.Repeat("2", 40)}
	h := deliveryHandler(t, f, true)
	createDeliverySnapshot(t, h)
	path := "/api/v1/changes/change-1/reviews"
	body := changeRequest(t, h, "POST", path, `{"idempotencyKey":"first","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201)
	first := readDeliveryResult(t, body)
	if first.Review.State != "PARTIAL" || first.Review.Services[1].ErrorCode != "review_findings_invalid" || first.Review.Services[1].ExecutionID == "" {
		t.Fatalf("lost partial failure: %s", body)
	}
	f.failHead = ""
	second := readDeliveryResult(t, changeRequest(t, h, "POST", path, fmt.Sprintf(`{"idempotencyKey":"retry","expectedVersion":%d,"snapshotId":"snapshot-1"}`, first.ChangeVersion), 201))
	if second.Review.State != "SUCCEEDED" || f.runs != 4 || len(f.verified) != 3 {
		t.Fatal("retry recreated successful work")
	}
	for i, item := range second.Review.Services {
		prior := first.Review.Services[i]
		if item.TaskID != prior.TaskID {
			t.Fatal("retry created another task")
		}
		if i != 1 && (!item.Reused || item.ExecutionID != prior.ExecutionID || item.ArtifactID != prior.ArtifactID) {
			t.Fatal("successful evidence changed")
		}
		if i == 1 && (item.Reused || item.ExecutionID == prior.ExecutionID) {
			t.Fatal("failed execution not retried")
		}
	}
	var old change.ReviewRun
	if err := json.Unmarshal(changeRequest(t, h, "GET", path+"/"+first.Review.ID, "", 200), &old); err != nil {
		t.Fatal(err)
	}
	if old.State != "PARTIAL" || old.Services[1].ArtifactID != "" {
		t.Fatal("history overwritten by retry")
	}
	// Even an identical new snapshot gets distinct tasks/evidence. Old results
	// cannot serve as a review for a new requirement version.
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", fmt.Sprintf(`{"idempotencyKey":"new-snapshot","expectedVersion":%d,"services":%s}`, second.ChangeVersion, deliverySelections), 201)
	third := readDeliveryResult(t, changeRequest(t, h, "POST", path, fmt.Sprintf(`{"idempotencyKey":"new-review","expectedVersion":%d,"snapshotId":"snapshot-2"}`, second.ChangeVersion+1), 201))
	if third.Review.SnapshotID != "snapshot-2" || f.runs != 7 {
		t.Fatal("new snapshot reused old conclusions")
	}
	for i, item := range third.Review.Services {
		if item.TaskID == second.Review.Services[i].TaskID || item.ExecutionID == second.Review.Services[i].ExecutionID {
			t.Fatal("snapshot evidence aliased")
		}
	}
	if replay := changeRequest(t, h, "POST", path, `{"idempotencyKey":"first","expectedVersion":2,"snapshotId":"snapshot-1"}`, 200); string(replay) != string(body) {
		t.Fatal("old replay changed")
	}
	changeRequest(t, h, "POST", path, fmt.Sprintf(`{"idempotencyKey":"again","expectedVersion":%d,"snapshotId":"snapshot-2"}`, third.ChangeVersion), 409)
	changeRequest(t, h, "POST", path, `{"idempotencyKey":"first","expectedVersion":2,"snapshotId":"snapshot-2"}`, 409)
}

func deliveryAs(h http.Handler, subject, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if subject != "" {
		token := subject + "-token-at-least-32-characters-long"
		if subject == "alice" {
			token = changeToken
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestChangeReviewAuthorizationAlsoProtectsLegacyTaskAndArtifactPaths(t *testing.T) {
	f := &deliveryFixture{}
	h := deliveryHandler(t, f, true)
	createDeliverySnapshot(t, h)
	path, body := "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`
	for subject, want := range map[string]int{"": 401, "reader": 403, "outsider": 403, "bob": 404} {
		if w := deliveryAs(h, subject, "POST", path, body); w.Code != want {
			t.Fatalf("%s: %d %s", subject, w.Code, w.Body.String())
		}
	}
	if f.runs != 0 || len(f.verified) != 0 {
		t.Fatal("unauthorized downstream work")
	}
	changeRequest(t, h, "POST", path, `{"idempotencyKey":"invalid","expectedVersion":1,"snapshotId":"snapshot-1"}`, 409)
	changeRequest(t, h, "POST", path, `{"idempotencyKey":"invalid","expectedVersion":2,"snapshotId":"unknown"}`, 409)
	for _, invalid := range []string{
		`{"idempotencyKey":"invalid","expectedVersion":2,"snapshotId":"snapshot-1","tenantId":"tenant-b"}`,
		`{"idempotencyKey":"invalid","expectedVersion":2,"snapshotId":"snapshot-1","repository":{"headSha":"forged"}}`,
		`{"idempotencyKey":"invalid","expectedVersion":2,"snapshotId":"snapshot-1","SnapshotId":"snapshot-2"}`,
		`{"idempotencyKey":"invalid","expectedVersion":2,"snapshotId":"snapshot-1","snapshotId":"snapshot-2"}`,
	} {
		if w := deliveryAs(h, "alice", "POST", path, invalid); w.Code != 400 && w.Code != 403 {
			t.Fatalf("invalid input accepted: %d", w.Code)
		}
	}
	result := readDeliveryResult(t, changeRequest(t, h, "POST", path, body, 201))
	item := result.Review.Services[0]
	paths := []string{
		"/api/v1/tasks/" + item.TaskID, "/api/v1/tasks/" + item.TaskID + "/events",
		"/api/v1/tasks/" + item.TaskID + "/workspace", "/api/v1/tasks/" + item.TaskID + "/workspace/diff",
		"/api/v1/tasks/" + item.TaskID + "/review", "/api/v1/tasks/" + item.TaskID + "/reviews/" + item.ExecutionID,
		"/api/v1/artifacts/" + item.ArtifactID, "/api/v1/artifacts/" + item.ArtifactID + "/content",
		path, path + "/" + result.Review.ID,
	}
	for _, p := range paths {
		if w := deliveryAs(h, "outsider", "GET", p+"?tenantId=tenant-a", ""); w.Code != 403 {
			t.Fatalf("leaked %s: %d", p, w.Code)
		}
		if w := deliveryAs(h, "reader", "GET", p+"?tenantId=tenant-a", ""); w.Code != 200 {
			t.Fatalf("cannot read %s: %d", p, w.Code)
		}
	}
	if w := deliveryAs(h, "outsider", "GET", "/api/v1/tasks?tenantId=tenant-a", ""); w.Body.String() != "{\"items\":[]}\n" {
		t.Fatal("list leaked linked tasks")
	}
	for _, suffix := range []string{"/workspace", "/workspace/prepare", "/review", "/artifacts/diff"} {
		changeRequest(t, h, "POST", "/api/v1/tasks/"+item.TaskID+suffix, `{}`, 409)
	}
	changeRequest(t, h, "PATCH", "/api/v1/tasks/"+item.TaskID, `{}`, 409)
	// Standalone idempotency scope cannot retrieve a linked task by its key.
	legacy := fmt.Sprintf(`{"requestId":"legacy","tenantId":"tenant-a","idempotencyKey":"change-1:snapshot-1:S1","type":"PR_REVIEW","goal":"需求 A","repository":{"provider":"gitlab","repositoryId":"1","headSha":%q}}`, strings.Repeat("1", 40))
	changeRequest(t, h, "POST", "/api/v1/tasks", legacy, 503)
	if f.runs != 3 {
		t.Fatal("reads or forbidden writes ran the model")
	}
}

func TestChangeReviewConcurrentReplayCancellationAndRecovery(t *testing.T) {
	entered := make(chan struct{})
	f := &deliveryFixture{run: func(ctx context.Context, in review.RunInput) error {
		if in.HeadSHA == strings.Repeat("2", 40) {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	h := deliveryHandler(t, f, true)
	createDeliverySnapshot(t, h)
	path, body := "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"review","expectedVersion":2,"snapshotId":"snapshot-1"}`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+changeToken)
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(first, r); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not start")
	}
	live := changeRequest(t, h, "GET", path+"/change-review-1", "", 200)
	var running change.ReviewRun
	if err := json.Unmarshal(live, &running); err != nil {
		t.Fatal(err)
	}
	if running.State != "RUNNING" || running.Services[0].State != "SUCCEEDED" || running.Services[1].State != "RUNNING" || running.Services[2].State != "NOT_STARTED" {
		t.Fatalf("incorrect live progress: %s", live)
	}
	changeRequest(t, h, "POST", path, `{"idempotencyKey":"competing","expectedVersion":3,"snapshotId":"snapshot-1"}`, 409)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"competing","expectedVersion":3,"services":`+deliverySelections+`}`, 409)
	replayDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { replayDone <- deliveryAs(h, "alice", "POST", path, body) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	var replay *httptest.ResponseRecorder
	select {
	case replay = <-replayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not finish")
	}
	if first.Code != 201 || replay.Code != 200 || first.Body.String() != replay.Body.String() {
		t.Fatal("concurrent replay diverged")
	}
	result := readDeliveryResult(t, first.Body.Bytes())
	if result.Review.State != "CANCELED" || result.Review.Services[0].ArtifactID == "" || result.Review.Services[2].TaskID != "" || f.runs != 2 {
		t.Fatal("cancellation lost success or started another service")
	}
	f.mu.Lock()
	f.run = nil
	f.mu.Unlock()
	resumed := readDeliveryResult(t, changeRequest(t, h, "POST", path, fmt.Sprintf(`{"idempotencyKey":"resume","expectedVersion":%d,"snapshotId":"snapshot-1"}`, result.ChangeVersion), 201))
	if resumed.Review.State != "SUCCEEDED" || f.runs != 4 || !resumed.Review.Services[0].Reused {
		t.Fatal("recovery reran successful service")
	}
}

func TestChangeReviewUnavailableDoesNotCreateTasks(t *testing.T) {
	f := &deliveryFixture{}
	h := deliveryHandler(t, f, false)
	createDeliverySnapshot(t, h)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"disabled","expectedVersion":2,"snapshotId":"snapshot-1"}`, 503)
	if got := changeRequest(t, h, "GET", "/api/v1/tasks?tenantId=tenant-a", "", 200); string(got) != "{\"items\":[]}\n" || len(f.verified) != 0 {
		t.Fatal("unavailable review created tasks")
	}
}

func TestChangeReviewRecoversAdmissionAndPreparationFailures(t *testing.T) {
	for _, stage := range []string{"registration", "preparation"} {
		t.Run(stage, func(t *testing.T) {
			f := &deliveryFixture{}
			if stage == "registration" {
				f.failVerify = "2"
			} else {
				f.failPrepare = "2"
			}
			h := deliveryHandler(t, f, true)
			createDeliverySnapshot(t, h)
			first := readDeliveryResult(t, changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"first","expectedVersion":2,"snapshotId":"snapshot-1"}`, 201))
			failed := first.Review.Services[1]
			if first.Review.State != "PARTIAL" || failed.ErrorCode != "workspace_"+stage+"_failed" || failed.ExecutionID != "" || f.runs != 2 {
				t.Fatalf("invalid failure record: %+v", failed)
			}
			if stage == "preparation" && failed.WorkspaceVersion != 3 {
				t.Fatal("failed preparation version lost")
			}
			f.failVerify, f.failPrepare = "", ""
			second := readDeliveryResult(t, changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", fmt.Sprintf(`{"idempotencyKey":"retry","expectedVersion":%d,"snapshotId":"snapshot-1"}`, first.ChangeVersion), 201))
			if second.Review.State != "SUCCEEDED" || f.runs != 3 || second.Review.Services[1].TaskID != failed.TaskID {
				t.Fatal("failed service could not resume its child task")
			}
			if stage == "preparation" && second.Review.Services[1].WorkspaceID != failed.WorkspaceID {
				t.Fatal("preparation retry replaced workspace")
			}
		})
	}
}

func TestChangeReviewRejectsFailedSnapshotButKeepsEarlierReadySnapshotUsable(t *testing.T) {
	f := &deliveryFixture{}
	h := deliveryHandler(t, f, true)
	createDeliverySnapshot(t, h)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", `{"idempotencyKey":"failed","expectedVersion":2,"services":`+strings.Replace(deliverySelections, "feature/A", "missing", 1)+`}`, 201)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"invalid","expectedVersion":3,"snapshotId":"snapshot-2"}`, 409)
	if got := changeRequest(t, h, "GET", "/api/v1/tasks?tenantId=tenant-a", "", 200); string(got) != "{\"items\":[]}\n" {
		t.Fatal("failed snapshot created tasks")
	}
	result := readDeliveryResult(t, changeRequest(t, h, "POST", "/api/v1/changes/change-1/reviews", `{"idempotencyKey":"ready","expectedVersion":3,"snapshotId":"snapshot-1"}`, 201))
	if result.Review.State != "SUCCEEDED" || result.Review.SnapshotID != "snapshot-1" {
		t.Fatal("failed snapshot disabled prior ready version")
	}
}
