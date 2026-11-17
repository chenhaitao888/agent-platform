package apiapp

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"agent-platform/backend/internal/connector/reference"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
)

type WriteConnectionSettings struct {
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	Kind           string            `json:"kind"`
	BaseURL        string            `json:"baseUrl"`
	CAFile         string            `json:"caFile,omitempty"`
	CredentialFile string            `json:"credentialFile"`
	Targets        []planning.Target `json:"targets"`
}
type WriteSettings struct {
	TemporalAddress   string                    `json:"temporalAddress"`
	Namespace         string                    `json:"namespace"`
	TaskQueue         string                    `json:"taskQueue"`
	AllowSelfApproval bool                      `json:"allowSelfApproval"`
	Connectors        []WriteConnectionSettings `json:"connectors"`
}
type writeRegistry struct {
	settings WriteSettings
	writers  map[string]resourcewrite.Writer
	targets  map[string][]planning.Target
	digest   string
}

// Explicit write settings never inherit a Reader's credential or target scope.
// The initial reference adapter requires the read/write endpoint and declared
// connection version to identify the same platform resource.
func readWriteSettings(config deployment, catalog *servicecatalog.Catalog) (writeRegistry, error) {
	result := writeRegistry{writers: map[string]resourcewrite.Writer{}, targets: map[string][]planning.Target{}}
	data, err := readPlanningFile(config.writesFile, config.root, 1<<20)
	if err != nil || strictjson.Decode(data, &result.settings) != nil || catalog == nil {
		return result, delivery.ErrConfiguration
	}
	s := result.settings
	if config.writeRouting != "" && config.writeRouting != writeRouting(s) {
		return result, delivery.ErrConfiguration
	}
	host, port, err := net.SplitHostPort(s.TemporalAddress)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || !planning.ValidIdentifier(s.Namespace) || !planning.ValidIdentifier(s.TaskQueue) || len(s.Connectors) == 0 || len(s.Connectors) > 64 {
		return result, delivery.ErrConfiguration
	}
	if config.simulationFile != "" {
		sim, err := readSimulationSettings(config)
		if err != nil || sim.TemporalAddress == s.TemporalAddress && sim.Namespace == s.Namespace && sim.TaskQueue == s.TaskQueue {
			return result, delivery.ErrConfiguration
		}
	}
	readers, err := readPlanningFile(config.planningFile, config.root, 1<<20)
	var paired struct {
		Connectors []planningConnection `json:"connectors"`
	}
	if err != nil || strictjson.Decode(readers, &paired) != nil {
		return result, delivery.ErrConfiguration
	}
	hashes := []string{planning.Hash(data), planning.Hash(readers)}
	for _, item := range s.Connectors {
		if _, exists := result.writers[item.ID]; exists {
			return result, delivery.ErrConfiguration
		}
		matched := false
		for _, reader := range paired.Connectors {
			if reader.ID == item.ID && reader.Kind == item.Kind && reader.Version == item.Version && reader.BaseURL == item.BaseURL && reader.CAFile == item.CAFile {
				matched = true
			}
		}
		if !matched || item.CredentialFile == "" {
			return result, delivery.ErrConfiguration
		}
		for _, target := range item.Targets {
			if !catalog.HasPlanningTarget(item.Kind, target) {
				return result, delivery.ErrConfiguration
			}
		}
		credentials, err := readPlanningFile(item.CredentialFile, config.root, 8192)
		var credential struct {
			Token string `json:"token"`
		}
		if err != nil || strictjson.Decode(credentials, &credential) != nil || strings.TrimSpace(credential.Token) == "" {
			return result, delivery.ErrConfiguration
		}
		hashes = append(hashes, planning.Hash(credentials))
		if item.CAFile != "" {
			ca, err := readPlanningFile(item.CAFile, config.root, 1<<20)
			if err != nil {
				return result, delivery.ErrConfiguration
			}
			hashes = append(hashes, planning.Hash(ca))
		}
		client, err := deploymentHTTPClient(item.CAFile, 5*time.Second)
		if err != nil {
			return result, delivery.ErrConfiguration
		}
		connection := reference.WriteConnection{Connection: reference.Connection{ID: item.ID, Version: item.Version, BaseURL: item.BaseURL, Token: credential.Token}, Targets: item.Targets}
		var writer resourcewrite.Writer
		switch item.Kind {
		case "config":
			writer, err = reference.NewConfigWriter(connection, client)
		case "experiment":
			writer, err = reference.NewExperimentWriter(connection, client)
		default:
			return result, delivery.ErrConfiguration
		}
		if err != nil {
			return result, delivery.ErrConfiguration
		}
		result.writers[item.ID] = writer
		result.targets[item.ID] = item.Targets
	}
	result.digest = planning.Hash(hashes)
	return result, nil
}
func (r writeRegistry) writer(kind string, target planning.Target) (resourcewrite.Writer, error) {
	w, ok := r.writers[target.ConnectorRef]
	if ok && w.Describe().Kind == kind {
		for _, allowed := range r.targets[target.ConnectorRef] {
			if allowed == target {
				return w, nil
			}
		}
	}
	return nil, delivery.ErrPolicy
}
func loadWrites(config deployment, store *delivery.Store) (*delivery.WriteService, WriteSettings, error) {
	if config.writesFile == "" {
		return nil, WriteSettings{}, nil
	}
	if store == nil || config.authFile == "" || config.catalogFile == "" || config.planningFile == "" || config.gitlabURL == "" {
		return nil, WriteSettings{}, delivery.ErrConfiguration
	}
	auth, err := loadAuthentication(config.authFile, config.root)
	if err != nil {
		return nil, WriteSettings{}, delivery.ErrConfiguration
	}
	catalog, err := loadServiceCatalog(config.catalogFile, config.root, auth, true)
	if err != nil {
		return nil, WriteSettings{}, delivery.ErrConfiguration
	}
	registry, err := readWriteSettings(config, catalog)
	if err != nil {
		return nil, WriteSettings{}, err
	}
	config.writeRouting = writeRouting(registry.settings)
	// Self approval is checked by the deployment policy on EVERY operation,
	// using the current private settings (whose default is false).
	return &delivery.WriteService{Store: store, Policy: writePolicy{config: config}, AllowSelfApproval: true}, registry.settings, nil
}
func writeRouting(s WriteSettings) string {
	return planning.Hash([]string{s.TemporalAddress, s.Namespace, s.TaskQueue})
}

// Dedicated entry point loads no Review runner, model key or inference runtime.
func WriteWorkerFromEnvironment(ctx context.Context) (*delivery.WriteService, WriteSettings, error) {
	config, err := loadDeployment()
	if err != nil {
		return nil, WriteSettings{}, err
	}
	auth, err := loadAuthentication(config.authFile, config.root)
	if err != nil {
		return nil, WriteSettings{}, err
	}
	catalog, err := loadServiceCatalog(config.catalogFile, config.root, auth, config.gitlabURL != "")
	if err != nil {
		return nil, WriteSettings{}, err
	}
	if _, err := loadPlanning(config.planningFile, config.root, catalog); err != nil {
		return nil, WriteSettings{}, err
	}
	store, err := loadArchives(ctx, config.archiveFile, config.root, catalog)
	if err != nil {
		return nil, WriteSettings{}, err
	}
	service, settings, err := loadWrites(config, store)
	if err != nil || service == nil {
		store.Close()
		if err == nil {
			err = delivery.ErrConfiguration
		}
		return nil, settings, err
	}
	return service, settings, nil
}
