package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestBusinessRoutesDenyUnauthenticatedRequestsByDefault(t *testing.T) {
	for _, target := range []string{"/api/v1/tasks?tenantId=tenant-a", "/api/v1/tasks/task-1?tenantId=tenant-a", "/api/v1/artifacts/artifact-1/content?tenantId=tenant-a"} {
		response := httptest.NewRecorder()
		NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", target, response.Code)
		}
	}
}

const authTokenA = "tenant-a-test-token-12345678901234567890"
const authTokenB = "tenant-b-test-token-12345678901234567890"

func authCredential(subject, tenant, token string, repos ...string) Credential {
	sum := sha256.Sum256([]byte(token))
	return Credential{Subject: subject, TenantID: tenant, TokenSHA256: hex.EncodeToString(sum[:]), RepositoryIDs: repos}
}
func authenticatedHandler(t *testing.T, next http.Handler, tenant string) http.Handler {
	t.Helper()
	auth, err := NewAuthenticator([]Credential{authCredential("alice", tenant, authTokenA, "platform/review", "platform/project-7"), authCredential("bob", "tenant-b", authTokenB, "platform/review", "platform/project-7")})
	if err != nil {
		t.Fatal(err)
	}
	return auth.Wrap(next)
}
func authRequest(t *testing.T, handler http.Handler, method, target, body, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != want {
		t.Fatalf("%s %s: want %d, got %d %s", method, target, want, response.Code, response.Body.String())
	}
	return response
}

func TestAuthenticationRejectsInvalidCredentialsAndAmbiguousClaims(t *testing.T) {
	handler := authenticatedHandler(t, NewHandler(), "tenant-a")
	for _, header := range []string{"", "Basic " + authTokenA, "Bearer wrong", "Bearer " + strings.Repeat("x", 300), "Bearer  " + authTokenA, "Bearer " + authTokenA + ",Bearer " + authTokenB} {
		request := httptest.NewRequest("GET", "/api/v1/tasks?tenantId=tenant-a", nil)
		request.Header.Set("Authorization", header)
		request.Header.Set("X-Tenant-ID", "tenant-a")
		request.Header.Set("X-Forwarded-User", "alice")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 401 || response.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("invalid header or spoofed identity accepted")
		}
	}
	request := httptest.NewRequest("GET", "/api/v1/tasks?tenantId=tenant-a", nil)
	request.Header.Add("Authorization", "Bearer "+authTokenA)
	request.Header.Add("Authorization", "Bearer "+authTokenB)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("duplicate Authorization accepted")
	}
	authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-a", "", authTokenA, 200)
	authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-b", "", authTokenA, 403)
	authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-a&tenantId=tenant-b", "", authTokenA, 400)
	authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=%zz", "", authTokenA, 400)
	authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-a&access_token="+authTokenA, "", "", 401)
	authRequest(t, handler, "GET", "/api/v1/session", "", authTokenA, 200)
	authRequest(t, handler, "GET", "/healthz", "", "", 200)
	for _, claim := range []string{`"tenantId":"tenant-b","tenantId":"tenant-a"`, `"TenantID":"tenant-a"`, `"tenantId":null`, `"tenantId":"tenant-a","TenantId":"tenant-b"`} {
		authRequest(t, handler, "POST", "/api/v1/tasks", "{"+claim+"}", authTokenA, 400)
	}
}

func TestAuthenticatedTenantsCannotReadOrMutateEachOthersReviewResources(t *testing.T) {
	// Real Git/workspace/review/artifact code; only external CLI inference is replaced.
	fixture := newReviewExecutionFixture(t, "success", 5*time.Second)
	handler := authenticatedHandler(t, fixture.handler, "tenant-review")
	owned := func(method, target, body string, want int) *httptest.ResponseRecorder {
		return authRequest(t, handler, method, target, body, authTokenA, want)
	}
	reviewBody := `{"requestId":"review-auth","idempotencyKey":"review-auth","tenantId":"tenant-review","expectedWorkspaceVersion":3}`
	completed := owned("POST", "/api/v1/tasks/task-1/review", reviewBody, 201)
	var operation struct {
		Artifact struct {
			ID string `json:"id"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(completed.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	artifactURL := "/api/v1/artifacts/" + operation.Artifact.ID
	beforeTask := owned("GET", "/api/v1/tasks/task-1?tenantId=tenant-review", "", 200).Body.String()
	beforeEvents := owned("GET", "/api/v1/tasks/task-1/events?tenantId=tenant-review", "", 200).Body.String()
	beforeReview := owned("GET", "/api/v1/tasks/task-1/review?tenantId=tenant-review", "", 200).Body.String()
	for _, path := range []string{"/api/v1/tasks/task-1", "/api/v1/tasks/task-1/events", "/api/v1/tasks/task-1/workspace", "/api/v1/tasks/task-1/workspace/diff", "/api/v1/tasks/task-1/review", artifactURL, artifactURL + "/content"} {
		authRequest(t, handler, "GET", path+"?tenantId=tenant-review", "", "", 401)
		authRequest(t, handler, "GET", path+"?tenantId=tenant-review", "", authTokenB, 403)
		authRequest(t, handler, "GET", path+"?tenantId=tenant-b", "", authTokenB, 404)
		owned("GET", path+"?tenantId=tenant-review", "", 200)
	}
	for _, route := range []struct{ method, path, fields string }{
		{"PATCH", "/api/v1/tasks/task-1", `,"expectedVersion":2,"status":"QUEUED"`},
		{"POST", "/api/v1/tasks/task-1/workspace", ""},
		{"POST", "/api/v1/tasks/task-1/workspace/prepare", `,"expectedVersion":3`},
		{"POST", "/api/v1/tasks/task-1/artifacts/diff", `,"expectedWorkspaceVersion":3`},
		{"POST", "/api/v1/tasks/task-1/review", `,"expectedWorkspaceVersion":3`},
	} {
		body := `{"requestId":"attack","idempotencyKey":"review-auth","tenantId":"tenant-review"` + route.fields + `}`
		authRequest(t, handler, route.method, route.path, body, "", 401)
		authRequest(t, handler, route.method, route.path, body, authTokenB, 403)
		authRequest(t, handler, route.method, route.path, strings.ReplaceAll(body, "tenant-review", "tenant-b"), authTokenB, 404)
	}
	create := fmt.Sprintf(`{"requestId":"create","idempotencyKey":"create","tenantId":"tenant-review","type":"PR_REVIEW","goal":"new","repository":{"provider":"gitlab","repositoryId":"platform/review","headSha":%q}}`, fixture.ready.HeadSHA)
	authRequest(t, handler, "POST", "/api/v1/tasks", create, authTokenB, 403)
	bob := authRequest(t, handler, "POST", "/api/v1/tasks", strings.ReplaceAll(create, "tenant-review", "tenant-b"), authTokenB, 201)
	if !strings.Contains(bob.Body.String(), `"tenantId":"tenant-b"`) || !strings.Contains(bob.Body.String(), `"id":"task-2"`) {
		t.Fatal("creation crossed tenant or reused another tenant's idempotency key")
	}
	if owned("GET", "/api/v1/tasks/task-1?tenantId=tenant-review", "", 200).Body.String() != beforeTask || owned("GET", "/api/v1/tasks/task-1/events?tenantId=tenant-review", "", 200).Body.String() != beforeEvents || owned("GET", "/api/v1/tasks/task-1/review?tenantId=tenant-review", "", 200).Body.String() != beforeReview || len(fixture.captures(t)) != 1 {
		t.Fatal("cross-tenant requests changed task/events/review or executed CLI")
	}
	listed := authRequest(t, handler, "GET", "/api/v1/tasks?tenantId=tenant-b", "", authTokenB, 200)
	if strings.Contains(listed.Body.String(), `"id":"task-1"`) {
		t.Fatal("list leaked another tenant")
	}
	// Revoking the repository stops new work on a previously prepared workspace.
	_, err := NewAuthenticator([]Credential{authCredential("alice", "tenant-review", authTokenA)})
	if err == nil {
		t.Fatal("nil repository list must not be accepted")
	}
	credential := authCredential("alice", "tenant-review", authTokenA)
	credential.RepositoryIDs = []string{}
	restricted, err := NewAuthenticator([]Credential{credential})
	if err != nil {
		t.Fatal(err)
	}
	authRequest(t, restricted.Wrap(fixture.handler), "POST", "/api/v1/tasks/task-1/review", reviewBody, authTokenA, 403)
}

func TestAuthenticationRejectsRepositoryBeforeResolutionAndRedactsLogs(t *testing.T) {
	calls := 0
	resolver := repositoryResolverFunc(func(context.Context, task.RepositoryReference) (task.RepositoryReference, error) {
		calls++
		return task.RepositoryReference{}, fmt.Errorf("%w: %s", repository.ErrVerificationUnavailable, authTokenA)
	})
	handler := authenticatedHandler(t, newHandlerWithTestResolver(resolver), "tenant-a")
	logs := captureJSONLogs(t)
	body := `{"requestId":"` + authTokenA + `","idempotencyKey":"test","tenantId":"tenant-a","type":"PR_REVIEW","goal":"review","repository":{"provider":"gitlab","repositoryId":"other/private","headSha":"` + testHeadSHA + `"}}`
	authRequest(t, handler, "POST", "/api/v1/tasks", body, authTokenA, 403)
	if calls != 0 {
		t.Fatal("unauthorized repository reached service credential")
	}
	response := authRequest(t, handler, "POST", "/api/v1/tasks", strings.ReplaceAll(body, "other/private", "platform/project-7"), authTokenA, 503)
	if calls != 1 || strings.Contains(logs.String(), authTokenA) || strings.Contains(response.Body.String(), authTokenA) {
		t.Fatal("API token was logged or echoed")
	}
}

func TestConcurrentAuthenticatedRequestsKeepIndependentTenants(t *testing.T) {
	handler := authenticatedHandler(t, NewHandlerWithRepositoryServices(repositoryVerifierFunc(func(context.Context, task.RepositoryReference) error { return nil }), ""), "tenant-a")
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			tenant, token := "tenant-a", authTokenA
			if i%2 == 1 {
				tenant, token = "tenant-b", authTokenB
			}
			body := fmt.Sprintf(`{"requestId":"create","idempotencyKey":"create-%d","tenantId":%q,"type":"PR_REVIEW","goal":"review",%s}`, i/2, tenant, testRepositoryJSON)
			request := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 201 || !strings.Contains(response.Body.String(), `"tenantId":"`+tenant+`"`) {
				t.Errorf("concurrent identity crossed: %d", response.Code)
			}
		}(i)
	}
	group.Wait()
	for _, pair := range []struct{ tenant, token string }{{"tenant-a", authTokenA}, {"tenant-b", authTokenB}} {
		response := authRequest(t, handler, "GET", "/api/v1/tasks?tenantId="+pair.tenant, "", pair.token, 200)
		var result struct {
			Items []task.Task `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 10 {
			t.Fatal("tenant idempotency keys collided")
		}
		for _, item := range result.Items {
			if item.TenantID != pair.tenant {
				t.Fatal("concurrent tenant data leak")
			}
		}
	}
}
