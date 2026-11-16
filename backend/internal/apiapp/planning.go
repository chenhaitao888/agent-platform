package apiapp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agent-platform/backend/internal/connector/reference"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/servicecatalog"
	"agent-platform/backend/internal/strictjson"
)

type planningConnection struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	Kind           string `json:"kind"`
	BaseURL        string `json:"baseUrl"`
	CAFile         string `json:"caFile,omitempty"`
	CredentialFile string `json:"credentialFile,omitempty"`
}

func loadPlanning(filename, root string, catalog *servicecatalog.Catalog) (*planning.Planner, error) {
	if filename == "" {
		return nil, catalog.ValidatePlanning(nil)
	}
	if catalog == nil {
		return nil, planning.ErrInvalid
	}
	data, err := readPlanningFile(filename, root, 1<<20)
	if err != nil {
		return nil, err
	}
	var config struct {
		Connectors []planningConnection `json:"connectors"`
	}
	if strictjson.Decode(data, &config) != nil || len(config.Connectors) == 0 || len(config.Connectors) > 64 {
		return nil, planning.ErrInvalid
	}
	configs := map[string]planning.ConfigReader{}
	experiments := map[string]planning.ExperimentReader{}
	seen := map[string]bool{}
	for _, item := range config.Connectors {
		if seen[item.ID] {
			return nil, planning.ErrInvalid
		}
		seen[item.ID] = true
		client, err := deploymentHTTPClient(item.CAFile, 5*time.Second)
		if err != nil {
			return nil, planning.ErrInvalid
		}
		connection := reference.Connection{ID: item.ID, Version: item.Version, BaseURL: item.BaseURL}
		if item.CredentialFile != "" {
			data, err := readPlanningFile(item.CredentialFile, root, 8192)
			if err != nil {
				return nil, err
			}
			var credential struct {
				Token string `json:"token"`
			}
			if strictjson.Decode(data, &credential) != nil || strings.TrimSpace(credential.Token) == "" {
				return nil, planning.ErrInvalid
			}
			connection.Token = credential.Token
		}
		switch item.Kind {
		case "config":
			r, err := reference.NewConfigReader(connection, client)
			if err != nil {
				return nil, err
			}
			configs[item.ID] = r
		case "experiment":
			r, err := reference.NewExperimentReader(connection, client)
			if err != nil {
				return nil, err
			}
			experiments[item.ID] = r
		default:
			return nil, planning.ErrInvalid
		}
	}
	planner, err := planning.NewPlanner(configs, experiments, planning.NewDiffAnalyzer())
	if err != nil {
		return nil, err
	}
	if err := catalog.ValidatePlanning(planner); err != nil {
		return nil, err
	}
	return planner, nil
}
func readPlanningFile(filename, workspaceRoot string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(filename) {
		return nil, planning.ErrInvalid
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return nil, planning.ErrInvalid
	}
	filename = filepath.Join(parent, filepath.Base(filename))
	root, err := resolvedFutureDirectory(workspaceRoot)
	if err != nil {
		return nil, planning.ErrInvalid
	}
	relative, err := filepath.Rel(root, filename)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return nil, planning.ErrInvalid
	}
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, planning.ErrInvalid
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > limit {
		return nil, planning.ErrInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, planning.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, planning.ErrInvalid
	}
	return data, nil
}
