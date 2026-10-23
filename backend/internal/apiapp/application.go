// Package apiapp assembles the API from deployment-owned configuration.
package apiapp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/httpapi"
)

type Application struct {
	Handler                  http.Handler
	ListenAddress            string
	ShutdownGrace            time.Duration
	ReviewEnabled            bool
	AuthenticationConfigured bool
}

type deployment struct {
	authFile                                                                                      string
	address, gitlabURL, gitlabToken, root, gitBinary, gitlabCA                                    string
	reviewEnabled                                                                                 bool
	dockerBinary, dockerHost, image, model, modelURL, principal, credentialFile, modelCA, seccomp string
	timeout                                                                                       time.Duration
}

func loadDeployment() (deployment, error) {
	value := func(key string) string { return strings.TrimSpace(os.Getenv("AGENT_PLATFORM_" + key)) }
	config := deployment{address: value("API_ADDR"), gitlabURL: value("GITLAB_BASE_URL"), gitlabToken: value("GITLAB_TOKEN"), root: value("WORKSPACE_ROOT"), gitBinary: value("GIT_BINARY"), gitlabCA: value("GITLAB_CA_FILE"),
		dockerBinary: value("DOCKER_BINARY"), dockerHost: value("DOCKER_HOST"), image: value("REVIEW_WORKER_IMAGE"), model: value("REVIEW_MODEL"), modelURL: value("MODEL_BASE_URL"), principal: value("MODEL_SERVICE_PRINCIPAL"), credentialFile: value("MODEL_CREDENTIAL_FILE"), modelCA: value("MODEL_CA_FILE"), seccomp: value("REVIEW_SECCOMP_POLICY"), timeout: 90 * time.Second}
	config.authFile = value("AUTH_FILE")
	enabled := value("REVIEW_ENABLED")
	if enabled != "" && enabled != "false" && enabled != "true" {
		return deployment{}, errors.New("AGENT_PLATFORM_REVIEW_ENABLED must be true or false")
	}
	config.reviewEnabled = enabled == "true"
	if config.reviewEnabled && (config.gitlabURL == "" || config.gitlabToken == "" || config.root == "" || config.dockerHost == "" || config.image == "" || config.model == "" || config.modelURL == "" || config.principal == "" || config.credentialFile == "") {
		return deployment{}, errors.New("AGENT_PLATFORM_REVIEW_ENABLED requires complete GitLab, workspace, Docker, model and service credential configuration")
	}
	if !config.reviewEnabled && (config.dockerBinary != "" || config.dockerHost != "" || config.image != "" || config.model != "" || config.modelURL != "" || config.principal != "" || config.credentialFile != "" || config.modelCA != "" || config.seccomp != "" || value("REVIEW_TIMEOUT") != "") {
		return deployment{}, errors.New("review deployment fields require AGENT_PLATFORM_REVIEW_ENABLED=true")
	}
	gitConfigured := config.gitlabURL != "" || config.gitlabToken != "" || config.root != "" || config.gitlabCA != "" || config.gitBinary != ""
	if gitConfigured && (config.gitlabURL == "" || config.gitlabToken == "" || config.root == "") {
		return deployment{}, errors.New("AGENT_PLATFORM_GITLAB_BASE_URL, AGENT_PLATFORM_GITLAB_TOKEN and AGENT_PLATFORM_WORKSPACE_ROOT must be configured together")
	}
	if timeout := value("REVIEW_TIMEOUT"); timeout != "" {
		parsed, err := time.ParseDuration(timeout)
		if err != nil || parsed < time.Second || parsed > 2*time.Minute {
			return deployment{}, errors.New("AGENT_PLATFORM_REVIEW_TIMEOUT must be between 1s and 2m")
		}
		config.timeout = parsed
	}
	if config.gitBinary == "" {
		config.gitBinary = "git"
	}
	if config.dockerBinary == "" {
		config.dockerBinary = "docker"
	}
	if config.address == "" {
		config.address = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(config.address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 0 || number > 65535 {
		return deployment{}, errors.New("AGENT_PLATFORM_API_ADDR must be a host and numeric port")
	}
	if net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return deployment{}, errors.New("API requires a loopback AGENT_PLATFORM_API_ADDR")
	}
	return config, nil
}

// FromEnvironment validates configuration before contacting deployment services.
// Startup never reads a model credential or performs inference.
func /**/ FromEnvironment(ctx context.Context) (*Application, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := loadDeployment()
	if err != nil {
		return nil, err
	}
	authentication, err := loadAuthentication(config.authFile, config.root)
	if err != nil {
		return nil, err
	}
	app := &Application{Handler: httpapi.NewHandler(), ListenAddress: config.address, ShutdownGrace: time.Minute, ReviewEnabled: config.reviewEnabled, AuthenticationConfigured: authentication != nil}
	if config.gitlabURL == "" {
		app.Handler = authentication.Wrap(app.Handler)
		return app, nil
	}
	client, err := deploymentHTTPClient(config.gitlabCA, 5*time.Second)
	if err != nil {
		return nil, err
	}
	verifier, err := gitlab.NewVerifier(config.gitlabURL, config.gitlabToken, client)
	if err != nil {
		return nil, err
	}
	preparer, err := gitworkspace.NewPreparerWithCAFile(config.gitBinary, config.root, config.gitlabCA)
	if err != nil {
		return nil, err
	}
	reader, err := gitworkspace.NewDiffReader(config.gitBinary, config.root)
	if err != nil {
		return nil, err
	}
	workspacePreparer, err := gitlab.NewWorkspacePreparer(verifier, preparer)
	if err != nil {
		return nil, err
	}
	if !config.reviewEnabled {
		app.Handler, err = httpapi.NewHandlerWithWorkspaceServices(verifier, workspacePreparer, reader, config.root, config.gitlabToken)
		if err != nil {
			return nil, err
		}
		app.Handler = authentication.Wrap(app.Handler)
		return app, nil
	}
	var modelClient *http.Client
	if config.modelCA != "" {
		modelClient, err = deploymentHTTPClient(config.modelCA, config.timeout)
		if err != nil {
			return nil, err
		}
	}
	// Root has already passed both Git adapters' absolute/non-root checks.
	if err := os.MkdirAll(config.root, 0o700); err != nil {
		return nil, errors.New("prepare deployment workspace root failed")
	}
	runner, err := codex.NewDockerRunner(ctx, codex.DockerConfig{Binary: config.dockerBinary, Endpoint: config.dockerHost, Image: config.image, WorkspaceRoot: config.root, Model: config.model, Timeout: config.timeout, UID: os.Getuid(), GID: os.Getgid(), SeccompPolicy: config.seccomp, Gateway: &codex.GatewayConfig{BaseURL: config.modelURL, ServicePrincipal: config.principal, CredentialFile: config.credentialFile}, ModelRelay: true, ModelHTTPClient: modelClient})
	if err != nil {
		return nil, err
	}
	app.Handler, err = httpapi.NewHandlerWithReviewServices(verifier, workspacePreparer, reader, runner, config.root, config.gitlabToken)
	if err != nil {
		return nil, err
	}
	underlying := app.Handler
	app.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/review") {
			requestContext, cancel := context.WithTimeout(r.Context(), config.timeout)
			defer cancel()
			r = r.WithContext(requestContext)
		}
		underlying.ServeHTTP(w, r)
	})
	app.ShutdownGrace = max(time.Minute, config.timeout+30*time.Second)
	app.Handler = authentication.Wrap(app.Handler)
	return app, nil
}
