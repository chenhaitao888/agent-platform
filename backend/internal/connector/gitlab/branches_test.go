package gitlab_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/repository"
)

func TestBranchResolverRejectsMissingOrMalformedReferences(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		sourceStatus, targetStatus, baseStatus int
		body                                   string
		want                                   error
		calls                                  int
	}{
		{"source missing", 404, 200, 200, "", repository.ErrSourceBranchNotFound, 1},
		{"target missing", 200, 404, 200, "", repository.ErrTargetBranchNotFound, 2},
		{"no ancestor", 200, 200, 404, "", repository.ErrReviewBaseNotFound, 3},
		{"malformed SHA", 200, 200, 200, `{"commit":{"id":"short"}}`, repository.ErrVerificationUnavailable, 1},
		{"oversized", 200, 200, 200, strings.Repeat(" ", (1<<20)+1), repository.ErrVerificationUnavailable, 1},
		{"redirect", 302, 200, 200, "", repository.ErrVerificationUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch {
				case strings.HasSuffix(r.URL.EscapedPath(), "/branches/feature%2FA"):
					if tc.sourceStatus == 302 {
						w.Header().Set("Location", "/leak")
					}
					w.WriteHeader(tc.sourceStatus)
					if tc.body != "" {
						_, _ = w.Write([]byte(tc.body))
					} else {
						_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, strings.Repeat("a", 40))
					}
				case strings.HasSuffix(r.URL.EscapedPath(), "/branches/release%2Ftest"):
					w.WriteHeader(tc.targetStatus)
					_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, strings.Repeat("b", 40))
				case strings.HasSuffix(r.URL.Path, "/merge_base"):
					w.WriteHeader(tc.baseStatus)
					_, _ = fmt.Fprintf(w, `{"id":%q}`, strings.Repeat("c", 40))
				default:
					t.Error("unexpected downstream path")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			resolver, err := gitlab.NewVerifier(server.URL, "token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ResolveBranches(context.Background(), repository.BranchSelection{RepositoryID: "namespace/project", SourceBranch: "feature/A", TargetBranch: "release/test"})
			if !errors.Is(err, tc.want) || calls != tc.calls {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
