package apiapp

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"slices"
	"strconv"
	"time"

	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
)

type SimulationSettings struct {
	TemporalAddress   string `json:"temporalAddress"`
	Namespace         string `json:"namespace"`
	TaskQueue         string `json:"taskQueue"`
	AllowSelfApproval bool   `json:"allowSelfApproval"`
	LeaseSeconds      int    `json:"leaseSeconds,omitempty"`
}

func readSimulationSettings(config deployment) (SimulationSettings, error) {
	var settings SimulationSettings
	data, err := readPlanningFile(config.simulationFile, config.root, 8192)
	if err != nil || strictjson.Decode(data, &settings) != nil {
		return settings, delivery.ErrConfiguration
	}
	host, port, err := net.SplitHostPort(settings.TemporalAddress)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || !planning.ValidIdentifier(settings.Namespace) || !planning.ValidIdentifier(settings.TaskQueue) {
		return settings, delivery.ErrConfiguration
	}
	if settings.LeaseSeconds == 0 {
		settings.LeaseSeconds = 30
	}
	if settings.LeaseSeconds < 1 || settings.LeaseSeconds > 300 {
		return settings, delivery.ErrConfiguration
	}
	return settings, nil
}

type simulationPolicy struct{ config deployment }

func (p simulationPolicy) Authorize(ctx context.Context, subject string, b delivery.Bundle, operation string) (string, error) {
	if ctx.Err() != nil {
		return "", delivery.ErrUnavailable
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
	settings, err := readSimulationSettings(p.config)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	if operation == "approve_simulation" && !settings.AllowSelfApproval && (subject == b.CreatedBy || subject == b.Plan.CreatedBy) {
		return "", delivery.ErrPolicy
	}
	grant := operation
	if operation == "revoke_simulation" {
		grant = "approve_simulation"
	}
	entries := []servicecatalog.Entry{}
	for _, source := range b.Snapshot.Services {
		entry, ok := catalog.Authorized(actor, source.ServiceID, b.Environment, grant)
		if !ok || source.Repository == nil || !slices.Contains(actor.RepositoryIDs, source.Repository.RepositoryID) {
			return "", delivery.ErrPolicy
		}
		if operation != "read" && operation != "revoke_simulation" {
			if entry.CatalogVersion != source.CatalogVersion || entry.Repository.RepositoryID != source.Repository.RepositoryID || entry.Repository.ConnectorRef != source.ConnectorRef || !slices.Contains(entry.Repository.AllowedTargetBranches, source.TargetBranch) {
				return "", delivery.ErrPolicy
			}
			for _, action := range b.Plan.Actions {
				for _, proposal := range action.Proposals {
					if proposal.ServiceID != source.ServiceID {
						continue
					}
					matched := false
					for _, binding := range entry.Planning {
						if binding.Environment != b.Environment {
							continue
						}
						resource := binding.Config
						if action.Kind == "experiment" {
							resource = binding.Experiment
						}
						if resource != nil && resource.ConnectorRef == action.Target.ConnectorRef && resource.ResourceRef == action.Target.ResourceRef {
							matched = true
						}
					}
					if !matched {
						return "", delivery.ErrPolicy
					}
				}
			}
		}
		entries = append(entries, entry)
	}
	configuration, err := readPlanningFile(p.config.planningFile, p.config.root, 1<<20)
	if err != nil {
		return "", delivery.ErrPolicy
	}
	return planning.Hash(struct {
		Actor                       servicecatalog.Actor
		Entries                     []servicecatalog.Entry
		Connections                 string
		RepositoryURL, RepositoryCA string
		SelfApproval                bool
	}{actor, entries, planning.Hash(configuration), p.config.gitlabURL, p.config.gitlabCA, settings.AllowSelfApproval}), nil
}
func (p simulationPolicy) Revalidate(ctx context.Context, b delivery.Bundle) error {
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
	for _, s := range b.Snapshot.Services {
		ref, err := verifier.ResolveBranches(ctx, repository.BranchSelection{RepositoryID: s.Repository.RepositoryID, SourceBranch: s.SourceBranch, TargetBranch: s.TargetBranch})
		if err != nil {
			return delivery.ErrUnavailable
		}
		if ref != *s.Repository {
			return delivery.ErrDrift
		}
	}
	if err := planner.Revalidate(ctx, b.Plan); err != nil {
		if errors.Is(err, planning.ErrDrift) {
			return delivery.ErrDrift
		}
		return delivery.ErrUnavailable
	}
	return nil
}
func loadSimulation(config deployment, store *delivery.Store) (*delivery.SimulationService, SimulationSettings, error) {
	if config.simulationFile == "" {
		return nil, SimulationSettings{}, nil
	}
	if store == nil || config.authFile == "" || config.catalogFile == "" || config.planningFile == "" || config.gitlabURL == "" {
		return nil, SimulationSettings{}, delivery.ErrConfiguration
	}
	settings, err := readSimulationSettings(config)
	if err != nil {
		return nil, settings, err
	}
	return delivery.NewSimulationService(store, simulationPolicy{config: config}, "worker-"+rand.Text(), settings.LeaseSeconds), settings, nil
}

// Loads only policy, read connectors and ledger; no Review runner or model key.
func SimulationWorkerFromEnvironment(ctx context.Context) (*delivery.SimulationService, SimulationSettings, error) {
	config, err := loadDeployment()
	if err != nil {
		return nil, SimulationSettings{}, err
	}
	auth, err := loadAuthentication(config.authFile, config.root)
	if err != nil {
		return nil, SimulationSettings{}, err
	}
	catalog, err := loadServiceCatalog(config.catalogFile, config.root, auth, config.gitlabURL != "")
	if err != nil {
		return nil, SimulationSettings{}, err
	}
	if _, err := loadPlanning(config.planningFile, config.root, catalog); err != nil {
		return nil, SimulationSettings{}, err
	}
	store, err := loadArchives(ctx, config.archiveFile, config.root, catalog)
	if err != nil {
		return nil, SimulationSettings{}, err
	}
	s, settings, err := loadSimulation(config, store)
	if err != nil || s == nil {
		store.Close()
		if err == nil {
			err = delivery.ErrConfiguration
		}
		return nil, settings, err
	}
	return s, settings, nil
}
