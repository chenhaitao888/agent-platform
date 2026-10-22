package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var ErrInvalidAuthentication = errors.New("invalid API authentication configuration")
var identityName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
var bearerToken = regexp.MustCompile(`^[a-zA-Z0-9_-]{32,256}$`)

// Credential is deployment-owned. Raw tokens are never stored in this config.
type Credential struct {
	Subject       string   `json:"subject"`
	TenantID      string   `json:"tenantId"`
	TokenSHA256   string   `json:"tokenSha256"`
	RepositoryIDs []string `json:"repositoryIds"`
}

type Principal struct {
	Subject  string `json:"subject"`
	TenantID string `json:"tenantId"`
}

type authenticatedIdentity struct {
	principal    Principal
	repositories []string
	redact       func(string) string
}
type identityKey struct{}
type bearerBinding struct {
	identity authenticatedIdentity
	digest   [sha256.Size]byte
}
type Authenticator struct{ bindings []bearerBinding }

func NewAuthenticator(credentials []Credential) (*Authenticator, error) {
	if len(credentials) == 0 || len(credentials) > 64 {
		return nil, ErrInvalidAuthentication
	}
	a := &Authenticator{}
	seenSubjects, seenDigests := map[string]bool{}, map[string]bool{}
	tenantRepos := map[string][]string{}
	for _, credential := range credentials {
		digest, err := hex.DecodeString(credential.TokenSHA256)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(credential.TokenSHA256) != credential.TokenSHA256 || !identityName.MatchString(credential.Subject) || !identityName.MatchString(credential.TenantID) || seenSubjects[credential.Subject] || seenDigests[credential.TokenSHA256] || credential.RepositoryIDs == nil || len(credential.RepositoryIDs) > 128 {
			return nil, ErrInvalidAuthentication
		}
		seenSubjects[credential.Subject], seenDigests[credential.TokenSHA256] = true, true
		repositories := slices.Clone(credential.RepositoryIDs)
		slices.Sort(repositories)
		for i, repository := range repositories {
			if !isValidGitLabRepositoryID(repository) || (i > 0 && repositories[i-1] == repository) {
				return nil, ErrInvalidAuthentication
			}
		}
		if prior, exists := tenantRepos[credential.TenantID]; exists && !slices.Equal(prior, repositories) {
			return nil, ErrInvalidAuthentication
		}
		tenantRepos[credential.TenantID] = repositories
		binding := bearerBinding{identity: authenticatedIdentity{principal: Principal{Subject: credential.Subject, TenantID: credential.TenantID}, repositories: repositories}}
		copy(binding.digest[:], digest)
		a.bindings = append(a.bindings, binding)
	}
	return a, nil
}

// Wrap authenticates before routing, reading bodies, resolving repositories or
// accessing stores. A nil authenticator intentionally leaves business APIs closed.
func (a *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 || len(headers[0]) > 263 {
			unauthenticated(w)
			return
		}
		scheme, token, ok := strings.Cut(headers[0], " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || !bearerToken.MatchString(token) || a == nil {
			unauthenticated(w)
			return
		}
		digest := sha256.Sum256([]byte(token))
		index := -1
		for i, binding := range a.bindings {
			if subtle.ConstantTimeCompare(digest[:], binding.digest[:]) == 1 {
				index = i
			}
		}
		if index < 0 {
			unauthenticated(w)
			return
		}
		identity := a.bindings[index].identity
		identity.redact = func(value string) string { return strings.ReplaceAll(value, token, "[REDACTED]") }
		// Never pass the Authorization header to business code or outbound adapters.
		r = r.Clone(context.WithValue(r.Context(), identityKey{}, identity))
		r.Header.Del("Authorization")
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_error", "invalid query parameters")
			return
		}
		if values, exists := query["tenantId"]; exists {
			if len(values) != 1 {
				writeError(w, http.StatusBadRequest, "validation_error", "tenantId must occur once")
				return
			}
			if _, ok := checkedTenant(w, r, values[0]); !ok {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="agent-platform"`)
	writeError(w, http.StatusUnauthorized, "unauthenticated", "valid API credentials are required")
}

func requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.Header().Set("Cache-Control", "no-store")
			if _, ok := r.Context().Value(identityKey{}).(authenticatedIdentity); !ok {
				unauthenticated(w)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func checkedTenant(w http.ResponseWriter, r *http.Request, claimed string) (string, bool) {
	identity, ok := r.Context().Value(identityKey{}).(authenticatedIdentity)
	if !ok {
		unauthenticated(w)
		return "", false
	}
	claimed = strings.TrimSpace(claimed)
	if claimed == "" {
		message := "tenantId is required"
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			message = "requestId, idempotencyKey and tenantId are required"
		}
		writeError(w, http.StatusBadRequest, "validation_error", message)
		return "", false
	}
	if exceedsIdentifierLimit(claimed) {
		writeError(w, http.StatusBadRequest, "validation_error", "request fields exceed supported length")
		return "", false
	}
	if claimed != identity.principal.TenantID {
		writeError(w, http.StatusForbidden, "tenant_forbidden", "request tenant does not match authenticated tenant")
		return "", false
	}
	return identity.principal.TenantID, true
}

func queryTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["tenantId"]) > 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid tenant query")
		return "", false
	}
	return checkedTenant(w, r, query.Get("tenantId"))
}

func repositoryAllowed(w http.ResponseWriter, r *http.Request, repository string) bool {
	identity, ok := r.Context().Value(identityKey{}).(authenticatedIdentity)
	if !ok {
		unauthenticated(w)
		return false
	}
	if !slices.Contains(identity.repositories, repository) {
		writeError(w, http.StatusForbidden, "repository_forbidden", "repository is not authorized for this tenant")
		return false
	}
	return true
}

func (h *handler) authorizeTaskRepository(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	current, ok := h.tasks.Get(r.PathValue("id"), tenantID)
	if !ok {
		// Preserve the service's existing not-found response. It checks ownership
		// before any repository access; never inspect another tenant's policy.
		return true
	}
	return repositoryAllowed(w, r, current.Repository.RepositoryID)
}

func session(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(identityKey{}).(authenticatedIdentity)
	if !ok {
		unauthenticated(w)
		return
	}
	writeJSON(w, http.StatusOK, identity.principal)
}

// Tenant fields cannot rely on encoding/json's case-insensitive, last-key-wins
// behavior. Other request field validation remains with the existing handlers.
func unambiguousTenantClaim(content []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(content)))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return true
	}
	seen := false
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
		if strings.EqualFold(key, "tenantId") {
			if seen || key != "tenantId" || string(value) == "null" {
				return false
			}
			seen = true
		}
	}
	return true
}
