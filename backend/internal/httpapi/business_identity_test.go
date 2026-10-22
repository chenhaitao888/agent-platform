package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/workspace"
)

// Existing business tests simulate an ALREADY authenticated caller so their
// validation/concurrency assertions remain focused. This exists only in tests.
// Authentication regressions use the real Bearer middleware and never infer an
// identity from a claim. A real middleware's identity is never overwritten here.
func businessTestIdentity(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(identityKey{}).(authenticatedIdentity); ok {
			next.ServeHTTP(w, r)
			return
		}
		tenant := strings.TrimSpace(r.URL.Query().Get("tenantId"))
		var body struct {
			TenantID   string `json:"tenantId"`
			Repository struct {
				ID string `json:"repositoryId"`
			} `json:"repository"`
		}
		if r.Body != nil {
			original := r.Body
			data, _ := io.ReadAll(io.LimitReader(original, maxRequestBodyBytes+1))
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(data), original), original}
			_ = json.Unmarshal(data, &body)
			if tenant == "" {
				tenant = strings.TrimSpace(body.TenantID)
			}
		}
		if tenant == "" {
			tenant = "tenant-a"
		}
		identity := authenticatedIdentity{principal: Principal{Subject: "business-test", TenantID: tenant}, repositories: []string{body.Repository.ID, "platform/project-7", "platform/review", "platform/input-fixture"}}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	})
}

func businessTestHandler() http.Handler { return businessTestIdentity(NewHandler()) }
func businessTestRepositoryHandler(refs repository.ReferenceServices, token string) http.Handler {
	return businessTestIdentity(NewHandlerWithRepositoryServices(refs, token))
}
func businessTestWorkspacePreparer(refs repository.ReferenceServices, preparer workspace.Preparer, root, token string) (http.Handler, error) {
	handler, err := NewHandlerWithWorkspacePreparer(refs, preparer, root, token)
	return businessTestIdentity(handler), err
}
func businessTestWorkspaceServices(refs repository.ReferenceServices, preparer workspace.Preparer, reader repository.DiffReader, root, token string) (http.Handler, error) {
	handler, err := NewHandlerWithWorkspaceServices(refs, preparer, reader, root, token)
	return businessTestIdentity(handler), err
}
func businessTestReviewServices(refs repository.ReferenceServices, preparer workspace.Preparer, reader repository.DiffReader, runner review.Runner, root, token string) (http.Handler, error) {
	handler, err := NewHandlerWithReviewServices(refs, preparer, reader, runner, root, token)
	return businessTestIdentity(handler), err
}
