// Package servicecatalog holds deployment-owned mappings and explicit read-only
// change permissions. Catalog presence alone never grants access.
package servicecatalog

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"agent-platform/backend/internal/repository"
)

var ErrInvalidCatalog = errors.New("invalid service catalog configuration")
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

type Repository struct {
	ConnectorRef          string   `json:"connectorRef"`
	RepositoryID          string   `json:"repositoryId"`
	DefaultTargetBranch   string   `json:"defaultTargetBranch"`
	AllowedTargetBranches []string `json:"allowedTargetBranches"`
}
type Entry struct {
	TenantID       string     `json:"tenantId"`
	ServiceID      string     `json:"serviceId"`
	Name           string     `json:"name"`
	CatalogVersion string     `json:"catalogVersion"`
	Repository     Repository `json:"repository"`
	Environments   []string   `json:"environments"`
}
type Grant struct {
	Subject     string   `json:"subject"`
	TenantID    string   `json:"tenantId"`
	ServiceID   string   `json:"serviceId"`
	Environment string   `json:"environment"`
	Operations  []string `json:"operations"`
}
type Config struct {
	Services []Entry `json:"services"`
	Grants   []Grant `json:"grants"`
}

// Registry binds the catalog to the existing single GitLab instance and API identities.
type Registry interface {
	RepositoryRegistered(tenant, repositoryID string) bool
	SubjectRegistered(subject, tenant string) bool
}
type Catalog struct {
	entries map[[2]string]Entry
	grants  map[[4]string][]string
}

type Actor struct {
	Subject       string
	TenantID      string
	RepositoryIDs []string
}

// PublicEntry omits connection and repository coordinates from catalog responses.
type PublicEntry struct {
	ServiceID             string   `json:"serviceId"`
	Name                  string   `json:"name"`
	CatalogVersion        string   `json:"catalogVersion"`
	DefaultTargetBranch   string   `json:"defaultTargetBranch"`
	AllowedTargetBranches []string `json:"allowedTargetBranches"`
	Environments          []string `json:"environments"`
}

func New(config Config, registry Registry) (*Catalog, error) {
	c := &Catalog{entries: map[[2]string]Entry{}, grants: map[[4]string][]string{}}
	if registry == nil || config.Services == nil || config.Grants == nil || len(config.Services) > 256 || len(config.Grants) > 4096 {
		return nil, ErrInvalidCatalog
	}
	for _, e := range config.Services {
		key := [2]string{e.TenantID, e.ServiceID}
		_, exists := c.entries[key]
		invalidIdentity := !namePattern.MatchString(e.TenantID) || !namePattern.MatchString(e.ServiceID) ||
			strings.TrimSpace(e.Name) != e.Name || e.Name == "" || len(e.Name) > 256 || !namePattern.MatchString(e.CatalogVersion)
		invalidRepository := e.Repository.ConnectorRef != "gitlab" || !registry.RepositoryRegistered(e.TenantID, e.Repository.RepositoryID)
		invalidTargets := !repository.ValidBranch(e.Repository.DefaultTargetBranch) ||
			!slices.Contains(e.Repository.AllowedTargetBranches, e.Repository.DefaultTargetBranch) ||
			len(e.Repository.AllowedTargetBranches) > 32 || !unique(e.Repository.AllowedTargetBranches)
		if exists || invalidIdentity || invalidRepository || invalidTargets || len(e.Environments) == 0 || !unique(e.Environments) {
			return nil, ErrInvalidCatalog
		}
		for _, branch := range e.Repository.AllowedTargetBranches {
			if !repository.ValidBranch(branch) {
				return nil, ErrInvalidCatalog
			}
		}
		for _, env := range e.Environments {
			if !slices.Contains([]string{"dev", "test", "qa", "uat", "staging"}, env) {
				return nil, ErrInvalidCatalog
			}
		}
		c.entries[key] = clone(e)
	}
	for _, g := range config.Grants {
		key := [4]string{g.TenantID, g.Subject, g.ServiceID, g.Environment}
		e, ok := c.entries[[2]string{g.TenantID, g.ServiceID}]
		if _, exists := c.grants[key]; exists || !ok || !registry.SubjectRegistered(g.Subject, g.TenantID) || !slices.Contains(e.Environments, g.Environment) || len(g.Operations) == 0 || !unique(g.Operations) || !slices.Contains(g.Operations, "read") {
			return nil, ErrInvalidCatalog
		}
		for _, op := range g.Operations {
			if !slices.Contains([]string{"read", "create", "snapshot", "review"}, op) {
				return nil, ErrInvalidCatalog
			}
		}
		c.grants[key] = slices.Clone(g.Operations)
	}
	return c, nil
}
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func clone(e Entry) Entry {
	e.Environments = slices.Clone(e.Environments)
	e.Repository.AllowedTargetBranches = slices.Clone(e.Repository.AllowedTargetBranches)
	return e
}
func (c *Catalog) Authorized(actor Actor, service, env, operation string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	e, ok := c.entries[[2]string{actor.TenantID, service}]
	if !ok || !slices.Contains(actor.RepositoryIDs, e.Repository.RepositoryID) || !slices.Contains(c.grants[[4]string{actor.TenantID, actor.Subject, service, env}], operation) {
		return Entry{}, false
	}
	return clone(e), true
}
func (c *Catalog) List(actor Actor) []PublicEntry {
	result := []PublicEntry{}
	if c == nil {
		return result
	}
	for _, e := range c.entries {
		if e.TenantID != actor.TenantID {
			continue
		}
		environments := []string{}
		for _, env := range e.Environments {
			if _, ok := c.Authorized(actor, e.ServiceID, env, "read"); ok {
				environments = append(environments, env)
			}
		}
		if len(environments) > 0 {
			result = append(result, PublicEntry{ServiceID: e.ServiceID, Name: e.Name, CatalogVersion: e.CatalogVersion, DefaultTargetBranch: e.Repository.DefaultTargetBranch, AllowedTargetBranches: slices.Clone(e.Repository.AllowedTargetBranches), Environments: environments})
		}
	}
	slices.SortFunc(result, func(a, b PublicEntry) int { return strings.Compare(a.ServiceID, b.ServiceID) })
	return result
}
