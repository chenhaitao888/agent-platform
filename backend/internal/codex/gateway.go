package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"agent-platform/backend/internal/review"
)

var ErrGatewayCredentialUnavailable = errors.New("model service credential unavailable")

// GatewayConfig belongs to deployment, never a Task or RunInput. Startup does
// not read credentials or contact the gateway; executions read the selected file.
type GatewayConfig struct {
	BaseURL          string
	ServicePrincipal string
	CredentialFile   string
}

const maxGatewayCredentialBytes = 8192
const modelTokenEnvironment = "AGENT_PLATFORM_MODEL_TOKEN"

var servicePrincipalName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,127}$`)

func validatedGateway(config *GatewayConfig) (*GatewayConfig, error) {
	if config == nil {
		return nil, nil
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" ||
		strings.ContainsAny(config.BaseURL, "\r\n\x00") || !servicePrincipalName.MatchString(config.ServicePrincipal) ||
		(config.CredentialFile != "" && !filepath.IsAbs(config.CredentialFile)) {
		return nil, ErrInvalidRunnerConfig
	}
	copy := *config
	return &copy, nil
}

func (config *GatewayConfig) arguments() []string {
	if config == nil {
		return nil
	}
	quotedURL, _ := json.Marshal(config.BaseURL)
	return []string{
		"--config", `model_provider="agent_gateway"`,
		"--config", `model_providers.agent_gateway.name="Agent Platform gateway"`,
		"--config", "model_providers.agent_gateway.base_url=" + string(quotedURL),
		"--config", `model_providers.agent_gateway.env_key="AGENT_PLATFORM_MODEL_TOKEN"`,
		"--config", `model_providers.agent_gateway.wire_api="responses"`,
		"--config", `model_providers.agent_gateway.requires_openai_auth=false`,
		"--config", `model_providers.agent_gateway.supports_websockets=false`,
	}
}

type gatewayCredential struct {
	ServicePrincipal string     `json:"servicePrincipal"`
	Token            string     `json:"token"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
}

func (credential gatewayCredential) String() string   { return "[redacted service credential]" }
func (credential gatewayCredential) GoString() string { return credential.String() }

func parseGatewayCredential(content []byte, principal string, timeout time.Duration) (gatewayCredential, error) {
	var credential gatewayCredential
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if len(content) > maxGatewayCredentialBytes || decoder.Decode(&credential) != nil || decoder.Decode(new(any)) != io.EOF {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	if err := credential.validate(principal, timeout); err != nil {
		return gatewayCredential{}, err
	}
	return credential, nil
}

func (credential gatewayCredential) validate(principal string, timeout time.Duration) error {
	if credential.ServicePrincipal != principal || len(credential.Token) == 0 || len(credential.Token) > 4096 ||
		(credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now().Add(timeout))) {
		return ErrGatewayCredentialUnavailable
	}
	for _, character := range credential.Token {
		if character <= 32 || character >= 127 {
			return ErrGatewayCredentialUnavailable
		}
	}
	return nil
}

func gatewayCredentialLocation(config *GatewayConfig, root string) (string, error) {
	if config == nil || config.CredentialFile == "" {
		return "", nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(config.CredentialFile))
	if err != nil {
		return "", ErrInvalidRunnerConfig
	}
	path := filepath.Join(parent, filepath.Base(config.CredentialFile))
	relative, err := filepath.Rel(root, path)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", ErrInvalidRunnerConfig
	}
	return path, nil
}

func (config *GatewayConfig) credential(timeout time.Duration, root string) (gatewayCredential, error) {
	path, err := gatewayCredentialLocation(config, root)
	if err != nil {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxGatewayCredentialBytes {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Nlink != 1 || metadata.Uid != uint32(os.Getuid()) {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, maxGatewayCredentialBytes+1))
	if err != nil {
		return gatewayCredential{}, ErrGatewayCredentialUnavailable
	}
	return parseGatewayCredential(content, config.ServicePrincipal, timeout)
}

func findingsContainCredential(report review.FindingsReport, token string) bool {
	if token == "" {
		return false
	}
	for _, finding := range report.Findings {
		for _, field := range []string{finding.Title, finding.Description, finding.Path} {
			if strings.Contains(field, token) {
				return true
			}
		}
	}
	return false
}
