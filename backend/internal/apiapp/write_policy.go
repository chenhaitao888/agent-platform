package apiapp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/httpapi"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/servicecatalog"
)

type writePolicy struct{ config deployment }

func (p writePolicy) Authorize(ctx context.Context, subject string, b delivery.Bundle, operation string) (string, error) {
	if ctx.Err() != nil {
		return "", delivery.ErrUnavailable
	}
	if !slices.Contains([]string{"read", "approve_write", "execute_write", "revoke_write", "prepare_compensation", "approve_compensation", "execute_compensation"}, operation) {
		return "", delivery.ErrPolicy
	}
	auth, err := loadAuthentication(p.config.authFile, p.config.root)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	actor, ok := auth.ActorForSubject(subject, b.TenantID)
	if !ok {
		return "", delivery.ErrPolicy
	}
	catalog, err := loadServiceCatalog(p.config.catalogFile, p.config.root, auth, true)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	entries := []servicecatalog.Entry{}
	for _, source := range b.Snapshot.Services {
		entry, ok := catalog.Authorized(actor, source.ServiceID, b.Environment, operation)
		if !ok || source.Repository == nil || !slices.Contains(actor.RepositoryIDs, source.Repository.RepositoryID) {
			return "", delivery.ErrPolicy
		}
		if operation != "read" && operation != "revoke_write" {
			if entry.CatalogVersion != source.CatalogVersion || entry.Repository.RepositoryID != source.Repository.RepositoryID || entry.Repository.ConnectorRef != source.ConnectorRef || !slices.Contains(entry.Repository.AllowedTargetBranches, source.TargetBranch) {
				return "", delivery.ErrPolicy
			}
			for _, action := range b.Plan.Actions {
				for _, proposal := range action.Proposals {
					if proposal.ServiceID == source.ServiceID && !entryBindsAction(entry, b.Environment, action) {
						return "", delivery.ErrPolicy
					}
				}
			}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return "", delivery.ErrPolicy
	}
	// Reads and revocations remain available when a write connection is removed
	// or broken, provided the caller retains current service/repository rights.
	if operation == "read" || operation == "revoke_write" {
		return planning.Hash(struct {
			Actor   servicecatalog.Actor
			Entries []servicecatalog.Entry
		}{actor, entries}), nil
	}
	registry, err := readWriteSettings(p.config, catalog)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	if (operation == "approve_write" || operation == "approve_compensation") && !registry.settings.AllowSelfApproval && (subject == b.CreatedBy || subject == b.Plan.CreatedBy) {
		return "", delivery.ErrPolicy
	}
	for _, action := range b.Plan.Actions {
		if _, err := registry.writer(action.Kind, action.Target); err != nil {
			return "", delivery.ErrPolicy
		}
	}
	identity, err := readPlanningFile(p.config.authFile, p.config.root, 64<<10)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	caDigest := ""
	if p.config.gitlabCA != "" {
		ca, err := readPlanningFile(p.config.gitlabCA, p.config.root, 1<<20)
		if err != nil {
			return "", delivery.ErrPolicy
		}
		caDigest = planning.Hash(ca)
	}
	return planning.Hash(struct {
		Actor                                         servicecatalog.Actor
		Entries                                       []servicecatalog.Entry
		Writes, Identity, RepositoryURL, RepositoryCA string
	}{actor, entries, registry.digest, planning.Hash(identity), p.config.gitlabURL, caDigest}), nil
}

func (p writePolicy) ReadCompensationConfig(ctx context.Context, target planning.Target) (planning.Descriptor, planning.ConfigState, error) {
	auth, err := loadAuthentication(p.config.authFile, p.config.root)
	if err != nil {
		return planning.Descriptor{}, planning.ConfigState{}, delivery.ErrPolicy
	}
	catalog, err := loadServiceCatalog(p.config.catalogFile, p.config.root, auth, true)
	if err != nil {
		return planning.Descriptor{}, planning.ConfigState{}, delivery.ErrPolicy
	}
	planner, err := loadPlanning(p.config.planningFile, p.config.root, catalog)
	if err != nil || planner == nil {
		return planning.Descriptor{}, planning.ConfigState{}, delivery.ErrPolicy
	}
	return planner.ReadConfigState(ctx, target)
}
func entryBindsAction(entry servicecatalog.Entry, environment string, action planning.Action) bool {
	for _, binding := range entry.Planning {
		if binding.Environment != environment {
			continue
		}
		resource := binding.Config
		if action.Kind == "experiment" {
			resource = binding.Experiment
		}
		if resource != nil && resource.ConnectorRef == action.Target.ConnectorRef && resource.ResourceRef == action.Target.ResourceRef && action.Target.TenantID == entry.TenantID && action.Target.Environment == environment {
			return true
		}
	}
	return false
}
func (p writePolicy) Writer(ctx context.Context, kind string, target planning.Target) (resourcewrite.Writer, error) {
	if ctx.Err() != nil {
		return nil, delivery.ErrUnavailable
	}
	auth, err := loadAuthentication(p.config.authFile, p.config.root)
	if err != nil {
		return nil, delivery.ErrPolicy
	}
	catalog, err := loadServiceCatalog(p.config.catalogFile, p.config.root, auth, true)
	if err != nil {
		return nil, delivery.ErrPolicy
	}
	registry, err := readWriteSettings(p.config, catalog)
	if err != nil {
		return nil, delivery.ErrPolicy
	}
	return registry.writer(kind, target)
}
func (p writePolicy) Revalidate(ctx context.Context, b delivery.Bundle, prefix []resourcewrite.Completed) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	auth, err := loadAuthentication(p.config.authFile, p.config.root)
	if err != nil {
		return delivery.ErrPolicy
	}
	catalog, err := loadServiceCatalog(p.config.catalogFile, p.config.root, auth, true)
	if err != nil {
		return delivery.ErrPolicy
	}
	planner, err := loadPlanning(p.config.planningFile, p.config.root, catalog)
	if err != nil || planner == nil {
		return delivery.ErrPolicy
	}
	client, err := deploymentHTTPClient(p.config.gitlabCA, 5*time.Second)
	if err != nil {
		return delivery.ErrPolicy
	}
	verifier, err := gitlab.NewVerifier(p.config.gitlabURL, p.config.gitlabToken, client)
	if err != nil {
		return delivery.ErrPolicy
	}
	for _, source := range b.Snapshot.Services {
		if source.Repository == nil {
			return delivery.ErrCorrupt
		}
		ref, err := verifier.ResolveBranches(ctx, repository.BranchSelection{RepositoryID: source.Repository.RepositoryID, SourceBranch: source.SourceBranch, TargetBranch: source.TargetBranch})
		if err != nil {
			return delivery.ErrUnavailable
		}
		if ref != *source.Repository {
			return delivery.ErrDrift
		}
	}
	if err := resourcewrite.RevalidateRemaining(ctx, planner, b.Plan, prefix); err != nil {
		if errors.Is(err, planning.ErrDrift) {
			return delivery.ErrDrift
		}
		if errors.Is(err, resourcewrite.ErrInvalid) {
			return delivery.ErrCorrupt
		}
		return delivery.ErrUnavailable
	}
	return nil
}

// Reload credentials before delivery routing, not after the outer middleware
// has discarded the bearer token. Other workbench routes retain their existing
// startup-authentication behavior. Raw tokens never reach business code.
func wrapDeliveryAuthentication(config deployment, initial *httpapi.Authenticator, next http.Handler) http.Handler {
	fallback := initial.Wrap(next)
	if config.writesFile == "" {
		return fallback
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/session" || r.URL.Path == "/api/v1/deliveries" || strings.HasPrefix(r.URL.Path, "/api/v1/deliveries/") {
			auth, err := loadAuthentication(config.authFile, config.root)
			if err != nil {
				auth = nil
			}
			auth.Wrap(next).ServeHTTP(w, r)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}
