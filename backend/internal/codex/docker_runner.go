package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agent-platform/backend/internal/review"
)

var immutableImage = regexp.MustCompile(`^(?:sha256:[0-9a-f]{64}|[a-zA-Z0-9][a-zA-Z0-9._/:\-]*@sha256:[0-9a-f]{64})$`)
var sha256Digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DockerConfig belongs to deployment configuration, never a task request.
// UID must match the service account that owns the mode-0700 workspace.
type DockerConfig struct {
	Binary, Endpoint, Image, WorkspaceRoot, Model string
	Timeout                                       time.Duration
	UID, GID                                      int
	SeccompPolicy                                 string
}

type DockerProfile struct {
	ImageID         string  `json:"imageId"`
	Architecture    string  `json:"architecture"`
	CLI             Profile `json:"cli"`
	SandboxVerified bool    `json:"sandboxVerified"`
	SeccompPolicy   string  `json:"seccompPolicy"`
	SeccompSHA256   string  `json:"seccompSha256,omitempty"`
}

type DockerRunner struct {
	config  DockerConfig
	profile DockerProfile
	seccomp []byte
}

var _ review.Runner = (*DockerRunner)(nil)

// NewDockerRunner inspects a local immutable image and runs the image's metadata
// probe under the same container boundaries as review. It never starts inference.
func NewDockerRunner(ctx context.Context, config DockerConfig) (*DockerRunner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	policy, seccomp, policyDigest, err := deploymentSeccomp(config.SeccompPolicy)
	if err != nil {
		return nil, err
	}
	if !immutableImage.MatchString(config.Image) || !strings.HasPrefix(config.Endpoint, "unix:///") ||
		strings.ContainsAny(config.Endpoint, "\r\n\x00") || config.UID <= 0 || config.GID < 0 ||
		config.Timeout <= 0 || strings.TrimSpace(config.Model) == "" ||
		!filepath.IsAbs(config.WorkspaceRoot) || strings.ContainsAny(config.WorkspaceRoot, ",\r\n") {
		return nil, ErrInvalidRunnerConfig
	}
	root, err := filepath.EvalSymlinks(config.WorkspaceRoot)
	if err != nil || root == "/" || strings.ContainsAny(root, ",\r\n") {
		return nil, ErrInvalidRunnerConfig
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidRunnerConfig
	}
	binary, err := exec.LookPath(config.Binary)
	if err != nil {
		return nil, ErrInvalidRunnerConfig
	}
	config.Binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, ErrInvalidRunnerConfig
	}
	config.WorkspaceRoot = root
	runner := &DockerRunner{config: config, seccomp: seccomp, profile: DockerProfile{SeccompPolicy: policy, SeccompSHA256: policyDigest}}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := runner.command(startup, nil, 64<<10, "image", "inspect", "--", config.Image)
	if err != nil {
		return nil, err
	}
	var images []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
	}
	if json.Unmarshal(data, &images) != nil || len(images) != 1 || images[0].OS != "linux" ||
		!immutableImage.MatchString(images[0].ID) || !strings.HasPrefix(images[0].ID, "sha256:") ||
		(images[0].Architecture != "arm64" && images[0].Architecture != "amd64") {
		return nil, ErrIncompatibleRuntime
	}
	runner.profile.ImageID, runner.profile.Architecture = images[0].ID, images[0].Architecture
	response, err := runner.container(startup, "", nil, true)
	if err != nil {
		return nil, err
	}
	message, err := decodeWorkerResponse(response)
	if err == nil && message.ErrorCode == "runtime_sandbox_unavailable" {
		return nil, fmt.Errorf("%w: native read-only sandbox could not start", ErrIncompatibleRuntime)
	}
	if err != nil || message.Profile == nil || message.Report != nil || message.ErrorCode != "" || !message.SandboxVerified || !validWorkerProfile(*message.Profile) {
		return nil, ErrIncompatibleRuntime
	}
	runner.profile.CLI = *message.Profile
	runner.profile.SandboxVerified = true
	if err := startup.Err(); err != nil {
		return nil, err
	}
	return runner, nil
}

func validWorkerProfile(profile Profile) bool {
	if profile.BinaryPath != "/opt/codex/codex" || !sha256Digest.MatchString(profile.BinarySHA256) ||
		profile.Integration != "exec" || profile.Sandbox != "read-only" || !profile.Ephemeral ||
		profile.Version == "" || len(profile.Version) > 128 || strings.ContainsAny(profile.Version, " \t\r\n") {
		return false
	}
	for _, required := range []string{"--sandbox", "--ephemeral", "--output-schema", "--output-last-message", "--json", "--ignore-user-config", "--ignore-rules", "--no-daemon", "--ask-for-approval", "--model"} {
		found := false
		for _, flag := range profile.RequiredFlags {
			if flag == required {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (r *DockerRunner) Profile() DockerProfile {
	profile := r.profile
	profile.CLI.RequiredFlags = append([]string(nil), profile.CLI.RequiredFlags...)
	return profile
}

func (r *DockerRunner) RuntimeIdentity() review.RuntimeIdentity {
	return review.RuntimeIdentity{Integration: "docker-exec", ImageID: r.profile.ImageID, CodexVersion: r.profile.CLI.Version, BinarySHA256: r.profile.CLI.BinarySHA256, Model: r.config.Model, SeccompPolicy: r.profile.SeccompPolicy, SeccompSHA256: r.profile.SeccompSHA256}
}

func (r *DockerRunner) Run(ctx context.Context, input review.RunInput) (review.FindingsReport, error) {
	if err := ctx.Err(); err != nil {
		return review.FindingsReport{}, err
	}
	if err := input.Validate(); err != nil {
		return review.FindingsReport{}, err
	}
	worktree, err := resolveWorktree(r.config.WorkspaceRoot, input.WorktreePath)
	if err != nil {
		return review.FindingsReport{}, err
	}
	if strings.ContainsAny(worktree, ",\"\r\n") {
		return review.FindingsReport{}, review.ErrInvalidRunInput
	}
	request, err := json.Marshal(input)
	if err != nil {
		return review.FindingsReport{}, review.ErrInvalidRunInput
	}
	execution, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	data, err := r.container(execution, worktree, request, false)
	if err != nil {
		return review.FindingsReport{}, err
	}
	message, err := decodeWorkerResponse(data)
	if err != nil || message.Profile != nil || message.SandboxVerified || (message.Report == nil) == (message.ErrorCode == "") {
		return review.FindingsReport{}, review.ErrInvalidFindings
	}
	if message.ErrorCode != "" {
		return review.FindingsReport{}, workerError(message.ErrorCode)
	}
	// Reapply the platform contract after crossing the Docker process boundary.
	report, err := review.ParseFindings(message.Report, input.BaseSHA, input.HeadSHA)
	if err != nil {
		return review.FindingsReport{}, err
	}
	if err := execution.Err(); err != nil {
		return review.FindingsReport{}, err
	}
	return report, nil
}

func (r *DockerRunner) container(ctx context.Context, worktree string, input []byte, probe bool) (output []byte, resultErr error) {
	var policyPath string
	if len(r.seccomp) != 0 {
		directory, err := os.MkdirTemp("", "agent-platform-seccomp-")
		if err != nil {
			return nil, review.ErrExecutionFailed
		}
		defer func() { _ = os.RemoveAll(directory) }()
		policyPath = filepath.Join(directory, "profile.json")
		if err := os.WriteFile(policyPath, r.seccomp, 0o600); err != nil {
			return nil, review.ErrExecutionFailed
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, review.ErrExecutionFailed
	}
	name := fmt.Sprintf("agent-platform-review-%x", nonce)
	// A canceled client does not stop the daemon-owned container. Always remove
	// this invocation's container using a separate, bounded cleanup context.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := r.command(cleanup, nil, 4096, "rm", "--force", name)
		if err != nil {
			output = nil
			resultErr = errors.Join(resultErr, review.ErrExecutionFailed)
		}
	}()
	args := []string{"create", "--name", name, "--label=agent-platform.owner=review-worker",
		"--log-driver=none",
		"--pull=never", "--network=none", "--read-only", "--user=" + strconv.Itoa(r.config.UID) + ":" + strconv.Itoa(r.config.GID),
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=256m", "--cpus=1",
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--interactive",
		"--entrypoint=/usr/local/bin/reviewworker", "--env=PATH=/usr/bin:/bin", "--env=HOME=/tmp",
		"--env=TMPDIR=/tmp", "--env=CODEX_HOME=/tmp/codex"}
	root := "/workspaces"
	if policyPath != "" {
		args = append(args, "--security-opt=seccomp="+policyPath)
	}
	if !probe {
		workspace := filepath.Dir(worktree)
		args = append(args, "--mount", "type=bind,source="+workspace+",target="+workspace+",readonly", "--workdir", worktree)
		root = r.config.WorkspaceRoot
	}
	args = append(args, r.profile.ImageID, "--root", root, "--model", r.config.Model, "--timeout", r.config.Timeout.String())
	if probe {
		args = append(args, "--probe")
	} else {
		args = append(args, "--sha256", r.profile.CLI.BinarySHA256)
	}
	if _, err := r.command(ctx, nil, 4096, args...); err != nil {
		return nil, err
	}
	return r.command(ctx, input, review.MaxFindingsBytes+8192, "start", "--attach", "--interactive", name)
}

type boundedDockerOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedDockerOutput) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if len(data) > remaining {
		buffer.exceeded = true
		_, _ = buffer.buffer.Write(data[:remaining])
	} else {
		_, _ = buffer.buffer.Write(data)
	}
	return len(data), nil
}

func (r *DockerRunner) command(ctx context.Context, input []byte, limit int, args ...string) ([]byte, error) {
	home, err := os.MkdirTemp("", "agent-platform-docker-client-")
	if err != nil {
		return nil, review.ErrExecutionFailed
	}
	defer func() { _ = os.RemoveAll(home) }()
	command := exec.CommandContext(ctx, r.config.Binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "DOCKER_CONFIG=" + home, "DOCKER_HOST=" + r.config.Endpoint}
	command.Stdin = bytes.NewReader(input)
	command.Stderr = io.Discard
	buffer := boundedDockerOutput{limit: limit}
	command.Stdout = &buffer
	command.WaitDelay = time.Second
	err = command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, review.ErrExecutionFailed
	}
	if buffer.exceeded {
		return nil, review.ErrOutputTooLarge
	}
	return buffer.buffer.Bytes(), nil
}
