package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/servicecatalog"
)

const changeToken = "change-test-token-at-least-32-characters"

func TestChangeFreezesThreeServicesWithoutCreatingReviewTasks(t *testing.T) {
	calls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		project := strings.Split(r.URL.Path, "/")[4]
		var index int
		if _, err := fmt.Sscanf(project, "%d", &index); err != nil || index < 1 || index > 3 {
			t.Error("unexpected project")
			http.NotFound(w, r)
			return
		}
		head, target, base := strings.Repeat(fmt.Sprint(index+3), 40), strings.Repeat(fmt.Sprint(index+6), 40), strings.Repeat(project, 40)
		if r.Method != "GET" || r.Header.Get("PRIVATE-TOKEN") != "fixture-gitlab" {
			t.Error("unexpected downstream operation")
		}
		switch {
		case strings.Contains(r.URL.Path, "/branches/feature/A"):
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, head)
		case strings.Contains(r.URL.Path, "/branches/"):
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, target)
		case strings.HasSuffix(r.URL.Path, "/merge_base"):
			if strings.Join(r.URL.Query()["refs[]"], ",") != target+","+head {
				t.Error("merge-base did not use frozen SHAs")
			}
			_, _ = fmt.Fprintf(w, `{"id":%q}`, base)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	resolver, err := gitlab.NewVerifier(upstream.URL, "fixture-gitlab", upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthenticator([]Credential{{Subject: "alice", TenantID: "tenant-a", TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(changeToken))), RepositoryIDs: []string{"1", "2", "3"}}})
	if err != nil {
		t.Fatal(err)
	}
	config := servicecatalog.Config{}
	for i, target := range []string{"main", "release/test", "master"} {
		id := fmt.Sprintf("S%d", i+1)
		config.Services = append(config.Services, servicecatalog.Entry{TenantID: "tenant-a", ServiceID: id, Name: id, CatalogVersion: "1", Repository: servicecatalog.Repository{ConnectorRef: "gitlab", RepositoryID: fmt.Sprint(i + 1), DefaultTargetBranch: target, AllowedTargetBranches: []string{target}}, Environments: []string{"test"}})
		config.Grants = append(config.Grants, servicecatalog.Grant{Subject: "alice", TenantID: "tenant-a", ServiceID: id, Environment: "test", Operations: []string{"read", "create", "snapshot"}})
	}
	catalog, err := servicecatalog.New(config, auth)
	if err != nil {
		t.Fatal(err)
	}
	handler := auth.Wrap(WithChanges(NewHandler(), change.NewService(catalog, resolver)))
	selections := `[{"serviceId":"S1","sourceBranch":"feature/A"},{"serviceId":"S2","sourceBranch":"feature/A"},{"serviceId":"S3","sourceBranch":"feature/A"}]`
	created := changeRequest(t, handler, "POST", "/api/v1/changes", `{"idempotencyKey":"create-1","title":"需求 A","description":"验收说明","environment":"test","services":`+selections+`}`, 201)
	if calls != 0 {
		t.Fatal("create contacted GitLab")
	}
	var record change.ChangeRequest
	if err := json.Unmarshal(created, &record); err != nil {
		t.Fatal(err)
	}
	snapshot := changeRequest(t, handler, "POST", "/api/v1/changes/"+record.ID+"/snapshots", `{"idempotencyKey":"snapshot-1","expectedVersion":1,"services":`+selections+`}`, 201)
	if calls != 9 {
		t.Fatalf("got %d GitLab reads", calls)
	}
	var result change.SnapshotResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.State != "READY" || len(result.Snapshot.Services) != 3 {
		t.Fatalf("incomplete snapshot: %s", snapshot)
	}
	for i, item := range result.Snapshot.Services {
		if item.Repository == nil || item.Repository.RepositoryID != fmt.Sprint(i+1) || item.Repository.TargetBranch != []string{"main", "release/test", "master"}[i] || item.Repository.HeadSHA != strings.Repeat(fmt.Sprint(i+4), 40) || item.Repository.TargetSHA != strings.Repeat(fmt.Sprint(i+7), 40) || item.Repository.BaseSHA != strings.Repeat(fmt.Sprint(i+1), 40) || item.ResolvedAt.IsZero() {
			t.Fatalf("invalid frozen service: %+v", item)
		}
	}
	replay := changeRequest(t, handler, "POST", "/api/v1/changes/"+record.ID+"/snapshots", `{"idempotencyKey":"snapshot-1","expectedVersion":1,"services":`+selections+`}`, 200)
	if string(snapshot) != string(replay) || calls != 9 {
		t.Fatal("snapshot replay drifted")
	}
	tasks := changeRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-a", "", 200)
	if string(tasks) != "{\"items\":[]}\n" {
		t.Fatalf("D1 created review tasks: %s", tasks)
	}
}

func changeRequest(t *testing.T, h http.Handler, method, path, body string, status int) []byte {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+changeToken)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s: want %d, got %d: %s", method, path, status, response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}
