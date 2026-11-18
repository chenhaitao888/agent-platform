package apiapp

import (
	"net/http"
	"time"

	"agent-platform/backend/internal/connector/mcpbridge"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/strictjson"
)

// MCPSettings is deployment-owned. The browser/model cannot set endpoints,
// credential paths, programs, target allowlists or the confirmed binding.
type MCPSettings struct {
	Connection  mcpbridge.Connection `json:"connection"`
	BindingFile string               `json:"bindingFile"`
	SkillFile   string               `json:"skillFile"`
	Targets     []planning.Target    `json:"targets"`
}

func ReadMCPSettings(filename, root string) (MCPSettings, error) {
	var s MCPSettings
	data, err := readPlanningFile(filename, root, 1<<20)
	if err != nil || strictjson.Decode(data, &s) != nil || len(s.Targets) == 0 || len(s.Targets) > 64 {
		return s, mcpbridge.ErrBinding
	}
	seen := map[planning.Target]bool{}
	for _, target := range s.Targets {
		if !resourcewrite.ValidTarget(target) || seen[target] {
			return s, mcpbridge.ErrBinding
		}
		seen[target] = true
	}
	return s, nil
}
func MCPClient(s MCPSettings, root string, write bool) (mcpbridge.Client, string, error) {
	c := mcpbridge.Client{Connection: s.Connection}
	hashes := []string{planning.Hash(s)}
	filename := s.Connection.ReadCredentialFile
	if write {
		filename = s.Connection.WriteCredentialFile
	}
	if filename != "" {
		data, err := readPlanningFile(filename, root, 16<<10)
		if err != nil || strictjson.Decode(data, &c.Credentials) != nil {
			return c, "", mcpbridge.ErrBinding
		}
		hashes = append(hashes, planning.Hash(data))
	}
	if s.Connection.CAFile != "" {
		data, err := readPlanningFile(s.Connection.CAFile, root, 1<<20)
		if err != nil {
			return c, "", mcpbridge.ErrBinding
		}
		hashes = append(hashes, planning.Hash(data))
	}
	client, err := deploymentHTTPClient(s.Connection.CAFile, 20*time.Second)
	if err != nil {
		return c, "", mcpbridge.ErrBinding
	}
	c.TLS = client.Transport.(*http.Transport).TLSClientConfig
	return c, planning.Hash(hashes), c.Validate()
}
func loadMCP(filename, root, id, version string, write bool) (*mcpbridge.Adapter, string, error) {
	s, err := ReadMCPSettings(filename, root)
	if err != nil || !planning.ValidIdentifier(id) || !planning.ValidIdentifier(version) {
		return nil, "", mcpbridge.ErrBinding
	}
	for _, target := range s.Targets {
		if target.ConnectorRef != id {
			return nil, "", mcpbridge.ErrBinding
		}
	}
	data, err := readPlanningFile(s.BindingFile, root, 1<<20)
	if err != nil {
		return nil, "", mcpbridge.ErrBinding
	}
	b, err := mcpbridge.ParseBinding(data)
	if err != nil || b.Validate(true) != nil {
		return nil, "", mcpbridge.ErrBinding
	}
	skill, err := readPlanningFile(s.SkillFile, root, 64<<10)
	if err != nil || planning.Hash(string(skill)) != b.SkillHash {
		return nil, "", mcpbridge.ErrBinding
	}
	r, readStamp, err := MCPClient(s, root, false)
	if err != nil {
		return nil, "", err
	}
	a := &mcpbridge.Adapter{ReadClient: r, Binding: b, Targets: s.Targets, Version: planning.Hash([]any{s, b, version})}
	writeStamp := ""
	if write {
		a.WriteClient, writeStamp, err = MCPClient(s, root, true)
		if err != nil {
			return nil, "", err
		}
	}
	return a, planning.Hash([]string{a.Version, readStamp, writeStamp}), nil
}

// ReadMCPPrivateFile shares the same owner, mode, symlink and workspace checks
// used by API/Worker configuration. The CLI does not bypass deployment checks.
func ReadMCPPrivateFile(filename, root string, limit int64) ([]byte, error) {
	return readPlanningFile(filename, root, limit)
}
