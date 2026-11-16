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
	"sync/atomic"
	"testing"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/task"
)

type branchResolverFunc func(context.Context, repository.BranchSelection) (task.RepositoryReference, error)

func (f branchResolverFunc) ResolveBranches(ctx context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
	return f(ctx, in)
}
func resolvedChangeReference(in repository.BranchSelection) task.RepositoryReference {
	return task.RepositoryReference{Provider: "gitlab", RepositoryID: in.RepositoryID, TargetBranch: in.TargetBranch, HeadSHA: strings.Repeat("a", 40), TargetSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("c", 40)}
}
func changeTestHandler(t *testing.T, resolver repository.BranchResolver) http.Handler {
	t.Helper()
	credentials := []Credential{}
	for _, v := range []struct {
		subject, tenant, token string
		repos                  []string
	}{{"alice", "tenant-a", changeToken, []string{"1", "2", "3"}}, {"limited", "tenant-a", strings.Repeat("l", 40), []string{"1", "2", "3"}}, {"bob", "tenant-b", strings.Repeat("b", 40), []string{"1", "2", "3"}}} {
		credentials = append(credentials, Credential{Subject: v.subject, TenantID: v.tenant, TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(v.token))), RepositoryIDs: v.repos})
	}
	auth, err := NewAuthenticator(credentials)
	if err != nil {
		t.Fatal(err)
	}
	config := servicecatalog.Config{Services: []servicecatalog.Entry{}, Grants: []servicecatalog.Grant{}}
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		for i := 1; i <= 3; i++ {
			id := fmt.Sprintf("S%d", i)
			config.Services = append(config.Services, servicecatalog.Entry{TenantID: tenant, ServiceID: id, Name: id, CatalogVersion: "v1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: fmt.Sprint(i), DefaultTargetBranch: "main", AllowedTargetBranches: []string{"main", "release/test"}}, Environments: []string{"test", "staging"}})
			for _, subject := range []string{"alice", "limited", "bob"} {
				if (subject == "bob") != (tenant == "tenant-b") || (subject == "limited" && i > 1) {
					continue
				}
				ops := []string{"read", "create", "snapshot"}
				if subject == "limited" {
					ops = []string{"read"}
				}
				config.Grants = append(config.Grants, servicecatalog.Grant{Subject: subject, TenantID: tenant, ServiceID: id, Environment: "test", Operations: ops})
			}
		}
	}
	catalog, err := servicecatalog.New(config, auth)
	if err != nil {
		t.Fatal(err)
	}
	return auth.Wrap(WithChanges(NewHandler(), change.NewService(catalog, resolver)))
}

const changeSelections = `[{"serviceId":"S1","sourceBranch":"feature/A"},{"serviceId":"S2","sourceBranch":"feature/B"},{"serviceId":"S3","sourceBranch":"feature/C"}]`
const changeCreateBody = `{"idempotencyKey":"create","title":"A","description":"acceptance","environment":"test","services":` + changeSelections + `}`

func freezeBody(key string, version int) string {
	return fmt.Sprintf(`{"idempotencyKey":%q,"expectedVersion":%d,"services":%s}`, key, version, changeSelections)
}
func rawChangeRequest(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestChangeAuthorizationPrecedesAllDownstreamReads(t *testing.T) {
	var calls atomic.Int32
	h := changeTestHandler(t, branchResolverFunc(func(_ context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
		calls.Add(1)
		return resolvedChangeReference(in), nil
	}))
	changeRequest(t, h, "POST", "/api/v1/changes", changeCreateBody, 201)
	tests := []struct {
		method, path, body, token string
		status                    int
	}{
		{"GET", "/api/v1/services", "", "", 401},
		{"POST", "/api/v1/changes", changeCreateBody, "", 401},
		{"POST", "/api/v1/changes/change-1/snapshots", freezeBody("s", 1), "", 401},
		{"GET", "/api/v1/changes?tenantId=tenant-b", "", changeToken, 403},
		{"POST", "/api/v1/changes", strings.Replace(changeCreateBody, `"title"`, `"tenantId":"tenant-b","title"`, 1), changeToken, 403},
		{"GET", "/api/v1/changes/change-1", "", strings.Repeat("b", 40), 404},
		{"POST", "/api/v1/changes/change-1/snapshots", freezeBody("s", 1), strings.Repeat("b", 40), 404},
		{"POST", "/api/v1/changes", strings.Replace(changeCreateBody, "S3", "unknown", 1), changeToken, 403},
		{"POST", "/api/v1/changes", strings.Replace(changeCreateBody, `"test"`, `"staging"`, 1), changeToken, 403},
		{"POST", "/api/v1/changes", changeCreateBody, strings.Repeat("l", 40), 403},
		{"GET", "/api/v1/changes/change-1", "", strings.Repeat("l", 40), 403},
		{"POST", "/api/v1/changes/change-1/snapshots", strings.Replace(freezeBody("s", 1), "S3", "unknown", 1), changeToken, 403},
		{"POST", "/api/v1/changes/change-1/snapshots", strings.Replace(freezeBody("s", 1), `"feature/C"`, `"feature/C","targetBranch":"production"`, 1), changeToken, 403},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s %s %d", tc.method, tc.path, tc.status), func(t *testing.T) {
			w := rawChangeRequest(h, tc.method, tc.path, tc.body, tc.token)
			if w.Code != tc.status {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized input contacted a repository")
	}
	services := rawChangeRequest(h, "GET", "/api/v1/services", "", strings.Repeat("l", 40)).Body.String()
	if !strings.Contains(services, "S1") || strings.Contains(services, "S2") || strings.Contains(services, "staging") || strings.Contains(services, "connectorRef") || strings.Contains(services, "repositoryId") {
		t.Fatalf("catalog leaks unauthorized information: %s", services)
	}
	list := rawChangeRequest(h, "GET", "/api/v1/changes", "", strings.Repeat("b", 40)).Body.String()
	if list != "{\"items\":[]}\n" {
		t.Fatal("cross tenant change listed")
	}
}
func TestChangeFailureRetryAndExplicitNewSnapshot(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var generation atomic.Int32
	generation.Store(1)
	var calls atomic.Int32
	h := changeTestHandler(t, branchResolverFunc(func(_ context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
		calls.Add(1)
		if in.RepositoryID == "2" && fail.Load() {
			return task.RepositoryReference{}, fmt.Errorf("%w: private token", repository.ErrSourceBranchNotFound)
		}
		r := resolvedChangeReference(in)
		if generation.Load() == 2 {
			r.HeadSHA = strings.Repeat("d", 40)
		}
		return r, nil
	}))
	changeRequest(t, h, "POST", "/api/v1/changes", changeCreateBody, 201)
	failed := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s1", 1), 201)
	var result change.SnapshotResult
	_ = json.Unmarshal(failed, &result)
	if result.Snapshot.State != "FAILED" || result.Snapshot.Services[1].ErrorCode != "source_branch_not_found" || strings.Contains(string(failed), "private token") {
		t.Fatalf("bad failure: %s", failed)
	}
	detail := changeRequest(t, h, "GET", "/api/v1/changes/change-1", "", 200)
	if strings.Contains(string(detail), "currentSnapshotId") {
		t.Fatal("partial snapshot became current")
	}
	fail.Store(false)
	replay := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s1", 1), 200)
	if string(replay) != string(failed) || calls.Load() != 3 {
		t.Fatal("failed operation replay was executed again")
	}
	first := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s2", 2), 201)
	generation.Store(2)
	replay = changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s2", 2), 200)
	if string(replay) != string(first) || calls.Load() != 6 {
		t.Fatal("successful replay moved with branch")
	}
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s3", 2), 409)
	newer := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", strings.Replace(freezeBody("s3", 3), "feature/A", "feature/new", 1), 201)
	if !strings.Contains(string(newer), strings.Repeat("d", 40)) {
		t.Fatal("new snapshot did not resolve moved branch")
	}
	detail = changeRequest(t, h, "GET", "/api/v1/changes/change-1", "", 200)
	if !strings.Contains(string(detail), strings.Repeat("a", 40)) || !strings.Contains(string(detail), "feature/A") {
		t.Fatal("historical snapshot was changed")
	}
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s3", 3), 409)
	fail.Store(true)
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s4", 4), 201)
	detail = changeRequest(t, h, "GET", "/api/v1/changes/change-1", "", 200)
	var afterFailure change.ChangeRequest
	if err := json.Unmarshal(detail, &afterFailure); err != nil {
		t.Fatal(err)
	}
	if afterFailure.CurrentSnapshotID != "snapshot-3" || len(afterFailure.Snapshots) != 4 || afterFailure.Snapshots[3].State != "FAILED" {
		t.Fatal("failed new snapshot replaced a historical success")
	}

}
func TestChangeConcurrentCreatesAndSnapshots(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := changeTestHandler(t, branchResolverFunc(func(_ context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return resolvedChangeReference(in), nil
	}))
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 16)
	for range 16 {
		wg.Go(func() { results <- rawChangeRequest(h, "POST", "/api/v1/changes", changeCreateBody, changeToken) })
	}
	wg.Wait()
	close(results)
	created := 0
	for r := range results {
		if r.Code == 201 {
			created++
		} else if r.Code != 200 {
			t.Fatalf("create: %d %s", r.Code, r.Body.String())
		}
		if !strings.Contains(r.Body.String(), `"id":"change-1"`) {
			t.Fatal("duplicate change")
		}
	}
	if created != 1 {
		t.Fatalf("%d creations", created)
	}
	changeRequest(t, h, "POST", "/api/v1/changes", strings.Replace(changeCreateBody, `"title":"A"`, `"title":"B"`, 1), 409)
	results = make(chan *httptest.ResponseRecorder, 16)
	for range 16 {
		wg.Go(func() {
			results <- rawChangeRequest(h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s", 1), changeToken)
		})
	}
	<-entered
	conflict := rawChangeRequest(h, "POST", "/api/v1/changes/change-1/snapshots", strings.Replace(freezeBody("s", 1), "feature/A", "feature/X", 1), changeToken)
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatal("concurrent different content not rejected")
	}
	changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("other", 1), 409)
	close(release)
	wg.Wait()
	close(results)
	created = 0
	var first string
	for r := range results {
		if r.Code == 201 {
			created++
		} else if r.Code != 200 {
			t.Fatalf("snapshot: %d %s", r.Code, r.Body.String())
		}
		if first == "" {
			first = r.Body.String()
		} else if first != r.Body.String() {
			t.Fatal("concurrent replay differs")
		}
	}
	if calls.Load() != 3 || created != 1 {
		t.Fatalf("calls=%d creations=%d", calls.Load(), created)
	}
}
func TestChangeRejectsForgedCoordinatesAndAmbiguousJSON(t *testing.T) {
	h := changeTestHandler(t, nil)
	for _, body := range []string{
		strings.Replace(changeCreateBody, `"sourceBranch":"feature/A"`, `"sourceBranch":"feature/A","headSha":"forged"`, 1),
		strings.Replace(changeCreateBody, `"title":"A"`, `"title":"A","title":"B"`, 1),
		strings.Replace(changeCreateBody, `"title"`, `"Title"`, 1),
		strings.Replace(changeCreateBody, `"sourceBranch":"feature/A"`, `"sourceBranch":"../bad"`, 1),
		strings.Replace(changeCreateBody, `"environment":"test"`, `"environment":null`, 1),
		changeCreateBody + `{}`,
	} {
		changeRequest(t, h, "POST", "/api/v1/changes", body, 400)
	}
}
func TestChangeRepositoryFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{repository.ErrTargetBranchNotFound, "target_branch_not_found"}, {repository.ErrReviewBaseNotFound, "review_base_not_found"}, {repository.ErrRepositoryNotFound, "repository_not_found"}, {errors.New("sensitive upstream response"), "repository_resolution_unavailable"}} {
		t.Run(tc.code, func(t *testing.T) {
			h := changeTestHandler(t, branchResolverFunc(func(context.Context, repository.BranchSelection) (task.RepositoryReference, error) {
				return task.RepositoryReference{}, tc.err
			}))
			changeRequest(t, h, "POST", "/api/v1/changes", changeCreateBody, 201)
			result := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s", 1), 201)
			if !strings.Contains(string(result), tc.code) || !strings.Contains(string(result), `"state":"FAILED"`) || strings.Contains(string(result), "sensitive") {
				t.Fatal(string(result))
			}
		})
	}
}

func TestChangeCancellationRetainsFailureAndAllowsExplicitRetry(t *testing.T) {
	entered := make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	h := changeTestHandler(t, branchResolverFunc(func(ctx context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
		if first.Swap(false) {
			close(entered)
			<-ctx.Done()
			return task.RepositoryReference{}, ctx.Err()
		}
		return resolvedChangeReference(in), nil
	}))
	changeRequest(t, h, "POST", "/api/v1/changes", changeCreateBody, 201)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("POST", "/api/v1/changes/change-1/snapshots", strings.NewReader(freezeBody("canceled", 1))).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+changeToken)
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), request) }()
	<-entered
	cancel()
	<-done
	result := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("canceled", 1), 200)
	if !strings.Contains(string(result), `"state":"FAILED"`) || !strings.Contains(string(result), "resolution_canceled") {
		t.Fatal(string(result))
	}
	retry := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("retry", 2), 201)
	if !strings.Contains(string(retry), `"state":"READY"`) {
		t.Fatal(string(retry))
	}
}

func TestChangeRejectsResolverReturningUntrustedCoordinates(t *testing.T) {
	h := changeTestHandler(t, branchResolverFunc(func(_ context.Context, in repository.BranchSelection) (task.RepositoryReference, error) {
		r := resolvedChangeReference(in)
		r.RepositoryID = "999"
		return r, nil
	}))
	changeRequest(t, h, "POST", "/api/v1/changes", changeCreateBody, 201)
	result := changeRequest(t, h, "POST", "/api/v1/changes/change-1/snapshots", freezeBody("s", 1), 201)
	var response change.SnapshotResult
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	if response.Snapshot.State != "FAILED" {
		t.Fatal("invalid coordinates published a successful snapshot")
	}
	for _, service := range response.Snapshot.Services {
		if service.Repository != nil || service.ErrorCode != "repository_resolution_unavailable" {
			t.Fatal("resolver coordinates were trusted without validation")
		}
	}
}
