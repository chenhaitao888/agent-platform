package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestVerifierAcceptsAnExistingRepositoryReference(t *testing.T) {
	var requestedPaths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := r.Header.Get("PRIVATE-TOKEN"); token != "test-token" {
			t.Errorf("expected service token, got %q", token)
		}
		requestedPaths = append(requestedPaths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	verifier, err := NewVerifier(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	err = verifier.Verify(context.Background(), task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "platform/agent-project",
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if err != nil {
		t.Fatalf("verify reference: %v", err)
	}

	expectedPaths := []string{
		"/api/v4/projects/platform%2Fagent-project",
		"/api/v4/projects/platform%2Fagent-project/repository/commits/1111111111111111111111111111111111111111",
		"/api/v4/projects/platform%2Fagent-project/repository/commits/2222222222222222222222222222222222222222",
	}
	if len(requestedPaths) != len(expectedPaths) {
		t.Fatalf("expected %d GitLab reads, got %d: %#v", len(expectedPaths), len(requestedPaths), requestedPaths)
	}
	for index, expected := range expectedPaths {
		if requestedPaths[index] != expected {
			t.Errorf("request %d: expected path %q, got %q", index+1, expected, requestedPaths[index])
		}
	}
}

func TestVerifierDoesNotForwardTheServiceTokenAcrossRedirects(t *testing.T) {
	var leakedToken string
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer redirectTarget.Close()

	gitlabServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer gitlabServer.Close()

	verifier, err := NewVerifier(gitlabServer.URL, "secret-service-token", gitlabServer.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	err = verifier.Verify(context.Background(), task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "project-7",
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if !errors.Is(err, repository.ErrVerificationUnavailable) {
		t.Fatalf("expected verification unavailable, got %v", err)
	}
	if leakedToken != "" {
		t.Fatalf("service token leaked across redirect: %q", leakedToken)
	}
}

func TestVerifierClassifiesMissingGitLabResources(t *testing.T) {
	tests := []struct {
		name          string
		notFoundCall  int
		expectedError error
	}{
		{name: "repository", notFoundCall: 1, expectedError: repository.ErrRepositoryNotFound},
		{name: "base commit", notFoundCall: 2, expectedError: repository.ErrBaseCommitNotFound},
		{name: "head commit", notFoundCall: 3, expectedError: repository.ErrHeadCommitNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call++
				if call == test.notFoundCall {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			verifier, err := NewVerifier(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("create verifier: %v", err)
			}
			err = verifier.Verify(context.Background(), task.RepositoryReference{
				Provider:     "gitlab",
				RepositoryID: "project-7",
				BaseSHA:      "1111111111111111111111111111111111111111",
				HeadSHA:      "2222222222222222222222222222222222222222",
			})
			if !errors.Is(err, test.expectedError) {
				t.Fatalf("expected %v, got %v", test.expectedError, err)
			}
			if call != test.notFoundCall {
				t.Fatalf("expected verification to stop after call %d, got %d calls", test.notFoundCall, call)
			}
		})
	}
}

func TestVerifierTreatsGitLabFailureAsUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()

	verifier, err := NewVerifier(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	err = verifier.Verify(context.Background(), task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "project-7",
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if !errors.Is(err, repository.ErrVerificationUnavailable) {
		t.Fatalf("expected verification unavailable, got %v", err)
	}
}

func TestNewVerifierRejectsIncompleteOrInsecureConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		token   string
		client  *http.Client
	}{
		{name: "plain HTTP", baseURL: "http://gitlab.example.com", token: "token", client: http.DefaultClient},
		{name: "embedded credentials", baseURL: "https://user:password@gitlab.example.com", token: "token", client: http.DefaultClient},
		{name: "query string", baseURL: "https://gitlab.example.com?target=other", token: "token", client: http.DefaultClient},
		{name: "fragment", baseURL: "https://gitlab.example.com#fragment", token: "token", client: http.DefaultClient},
		{name: "missing token", baseURL: "https://gitlab.example.com", client: http.DefaultClient},
		{name: "missing client", baseURL: "https://gitlab.example.com", token: "token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewVerifier(test.baseURL, test.token, test.client); err == nil {
				t.Fatal("expected invalid configuration to be rejected")
			}
		})
	}
}

func TestWorkspacePreparerUsesTrustedGitLabCloneURLAndEnvironmentCredentials(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/projects/platform%2Fagent-project" {
			t.Fatalf("unexpected GitLab path %q", r.URL.EscapedPath())
		}
		if token := r.Header.Get("PRIVATE-TOKEN"); token != "secret-service-token" {
			t.Fatalf("expected service token, got %q", token)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"http_url_to_repo":"` + server.URL + `/platform/agent-project.git"}`))
	}))
	defer server.Close()

	verifier, err := NewVerifier(server.URL, "secret-service-token", server.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	git := &capturingGitPreparer{}
	preparer, err := NewWorkspacePreparer(verifier, git)
	if err != nil {
		t.Fatalf("create Workspace preparer: %v", err)
	}
	reference := task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "platform/agent-project",
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	}
	destination := filepath.Join(t.TempDir(), "workspace-1", "worktree")

	if err := preparer.Prepare(context.Background(), reference, destination); err != nil {
		t.Fatalf("prepare Workspace: %v", err)
	}
	if git.calls != 1 {
		t.Fatalf("expected one Git preparation, got %d", git.calls)
	}
	if git.input.CloneURL != server.URL+"/platform/agent-project.git" {
		t.Fatalf("unexpected clone URL %q", git.input.CloneURL)
	}
	if git.input.BaseSHA != reference.BaseSHA || git.input.HeadSHA != reference.HeadSHA || git.input.Destination != destination {
		t.Fatalf("unexpected immutable checkout input: %#v", git.input)
	}
	if git.input.Credentials.Username != "oauth2" || git.input.Credentials.Password != "secret-service-token" {
		t.Fatalf("unexpected Git credentials: %#v", git.input.Credentials)
	}
}

func TestWorkspacePreparerRejectsACloneURLFromAnotherHost(t *testing.T) {
	otherServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer otherServer.Close()
	gitLabServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"http_url_to_repo":"` + otherServer.URL + `/platform/agent-project.git"}`))
	}))
	defer gitLabServer.Close()

	verifier, err := NewVerifier(gitLabServer.URL, "secret-service-token", gitLabServer.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	git := &capturingGitPreparer{}
	preparer, err := NewWorkspacePreparer(verifier, git)
	if err != nil {
		t.Fatalf("create Workspace preparer: %v", err)
	}
	err = preparer.Prepare(context.Background(), task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "platform/agent-project",
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	}, filepath.Join(t.TempDir(), "workspace-1", "worktree"))
	if !errors.Is(err, repository.ErrVerificationUnavailable) {
		t.Fatalf("expected untrusted clone URL to fail closed, got %v", err)
	}
	if git.calls != 0 {
		t.Fatalf("expected Git not to run for an untrusted URL, got %d calls", git.calls)
	}
}

type capturingGitPreparer struct {
	calls int
	input gitworkspace.Input
}

func (p *capturingGitPreparer) Prepare(_ context.Context, input gitworkspace.Input) error {
	p.calls++
	p.input = input
	return nil
}
