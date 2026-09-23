package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

func TestResolverFreezesMasterAndComputesReviewBase(t *testing.T) {
	const targetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const baseSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const headSHA = "cccccccccccccccccccccccccccccccccccccccc"
	var paths []string
	var mergeRefs []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "test-token" {
			t.Errorf("expected GitLab token, got %q", got)
		}
		paths = append(paths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/platform%2Fagent-project/repository/branches/master":
			_, _ = w.Write([]byte(`{"name":"master","commit":{"id":"` + targetSHA + `"}}`))
		case "/api/v4/projects/platform%2Fagent-project/repository/commits/" + headSHA:
			_, _ = w.Write([]byte(`{"id":"` + headSHA + `"}`))
		case "/api/v4/projects/platform%2Fagent-project/repository/merge_base":
			mergeRefs = r.URL.Query()["refs[]"]
			_, _ = w.Write([]byte(`{"id":"` + baseSHA + `"}`))
		default:
			t.Errorf("unexpected GitLab request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver, err := NewVerifier(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("create GitLab resolver: %v", err)
	}
	resolved, err := resolver.Resolve(context.Background(), task.RepositoryReference{
		Provider:     "gitlab",
		RepositoryID: "platform/agent-project",
		HeadSHA:      headSHA,
	})
	if err != nil {
		t.Fatalf("resolve review base: %v", err)
	}
	if resolved.TargetBranch != "master" || resolved.TargetSHA != targetSHA || resolved.BaseSHA != baseSHA || resolved.HeadSHA != headSHA {
		t.Fatalf("unexpected resolved reference: %#v", resolved)
	}
	if !reflect.DeepEqual(mergeRefs, []string{targetSHA, headSHA}) {
		t.Fatalf("merge-base must use the frozen target SHA and head, got %#v", mergeRefs)
	}
	if len(paths) != 3 || paths[0] != "/api/v4/projects/platform%2Fagent-project/repository/branches/master" || paths[1] != "/api/v4/projects/platform%2Fagent-project/repository/commits/"+headSHA || paths[2] != "/api/v4/projects/platform%2Fagent-project/repository/merge_base" {
		t.Fatalf("expected branch, head and merge-base reads, got %#v", paths)
	}
}

func TestResolverRejectsMissingOrMalformedGitLabReferences(t *testing.T) {
	const targetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const headSHA = "cccccccccccccccccccccccccccccccccccccccc"
	tests := []struct {
		name       string
		branchCode int
		branchBody string
		headCode   int
		baseCode   int
		baseBody   string
		want       error
	}{
		{"missing master", http.StatusNotFound, `{}`, http.StatusOK, http.StatusOK, `{}`, repository.ErrTargetBranchNotFound},
		{"invalid master SHA", http.StatusOK, `{"commit":{"id":"not-a-sha"}}`, http.StatusOK, http.StatusOK, `{}`, repository.ErrVerificationUnavailable},
		{"missing head", http.StatusOK, `{"commit":{"id":"` + targetSHA + `"}}`, http.StatusNotFound, http.StatusOK, `{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, repository.ErrHeadCommitNotFound},
		{"no common ancestor", http.StatusOK, `{"commit":{"id":"` + targetSHA + `"}}`, http.StatusOK, http.StatusNotFound, `{}`, repository.ErrReviewBaseNotFound},
		{"invalid merge-base SHA", http.StatusOK, `{"commit":{"id":"` + targetSHA + `"}}`, http.StatusOK, http.StatusOK, `{"id":"secret-token-instead-of-sha"}`, repository.ErrVerificationUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/api/v4/projects/42/repository/branches/master":
					w.WriteHeader(test.branchCode)
					_, _ = w.Write([]byte(test.branchBody))
				case "/api/v4/projects/42/repository/commits/" + headSHA:
					w.WriteHeader(test.headCode)
					_, _ = w.Write([]byte(`{"id":"` + headSHA + `"}`))
				case "/api/v4/projects/42/repository/merge_base":
					w.WriteHeader(test.baseCode)
					_, _ = w.Write([]byte(test.baseBody))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			resolver, err := NewVerifier(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatalf("create resolver: %v", err)
			}
			_, err = resolver.Resolve(context.Background(), task.RepositoryReference{
				Provider: "gitlab", RepositoryID: "42", HeadSHA: headSHA,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token-instead-of-sha") {
				t.Fatal("GitLab response body must not appear in resolver error")
			}
		})
	}
}
