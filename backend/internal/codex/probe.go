package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrIncompatibleRuntime = errors.New("incompatible Codex runtime")

var execOptionDeclaration = regexp.MustCompile(`^([ \t]*)(?:-[[:alnum:]],?[ \t]+)?(--[[:alnum:]][[:alnum:]-]*)(?:[ \t]+(?:<[^>]+>|\[[^]]+\])(?:\.\.\.)?)?[ \t]*$`)
var execPossibleValues = regexp.MustCompile(`(?m)^\[possible values:([^]]*)\][ \t]*$`)

const inspectionTimeout = 10 * time.Second

type Profile struct {
	Version       string   `json:"version"`
	BinaryPath    string   `json:"binaryPath"`
	BinarySHA256  string   `json:"binarySha256"`
	Integration   string   `json:"integration"`
	Sandbox       string   `json:"sandbox"`
	Ephemeral     bool     `json:"ephemeral"`
	RequiredFlags []string `json:"requiredFlags"`
}

// Probe verifies a configured CLI without starting a model run or reusing user auth.
// It records the actual version and digest and checks advertised capabilities.
// If supplied, expectedSHA256 must match before the binary is executed.
// Version and help share a ten-second inspection deadline, shortened by ctx.
func Probe(ctx context.Context, binary, expectedSHA256 string) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	if expectedSHA256 != "" {
		if digest, err := hex.DecodeString(expectedSHA256); err != nil || len(digest) != sha256.Size || strings.ToLower(expectedSHA256) != expectedSHA256 {
			return Profile{}, fmt.Errorf("%w: expected SHA-256 must be 64 lowercase hexadecimal characters", ErrIncompatibleRuntime)
		}
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return Profile{}, fmt.Errorf("find Codex binary: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return Profile{}, err
	}
	actualSHA256, err := binarySHA256(resolved)
	if err != nil {
		return Profile{}, err
	}
	if expectedSHA256 != "" && actualSHA256 != expectedSHA256 {
		return Profile{}, fmt.Errorf("%w: binary SHA-256 differs from the configured pin", ErrIncompatibleRuntime)
	}
	isolatedHome, err := os.MkdirTemp("", "agent-platform-codex-probe-")
	if err != nil {
		return Profile{}, err
	}
	defer func() { _ = os.RemoveAll(isolatedHome) }()
	inspectionContext, cancel := context.WithTimeout(ctx, inspectionTimeout)
	defer cancel()
	inspect := func(args ...string) (string, error) {
		return inspectCLI(inspectionContext, resolved, isolatedHome, args...)
	}
	version, err := inspect("--version")
	if err != nil {
		return Profile{}, fmt.Errorf("inspect Codex version: %w", err)
	}
	identity := strings.Fields(version)
	if len(identity) != 2 || identity[0] != "codex-cli" {
		return Profile{}, fmt.Errorf("%w: expected codex-cli version output", ErrIncompatibleRuntime)
	}
	help, err := inspect("exec", "--help")
	if err != nil {
		return Profile{}, fmt.Errorf("inspect Codex exec options: %w", err)
	}
	declared := execOptionBlocks(help)
	required := []string{"--sandbox", "--ephemeral", "--output-schema", "--output-last-message", "--json", "--ignore-user-config", "--ignore-rules"}
	for _, flag := range required {
		if _, ok := declared[flag]; !ok {
			return Profile{}, fmt.Errorf("%w: missing required option %s", ErrIncompatibleRuntime, flag)
		}
	}
	if !optionAcceptsValue(declared["--sandbox"], "read-only") {
		return Profile{}, fmt.Errorf("%w: read-only sandbox is not advertised", ErrIncompatibleRuntime)
	}
	return Profile{
		Version: identity[1], BinaryPath: resolved, BinarySHA256: actualSHA256,
		Integration: "exec", Sandbox: "read-only", Ephemeral: true, RequiredFlags: required,
	}, nil
}

func binarySHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", fmt.Errorf("hash Codex binary: %w", err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func inspectCLI(ctx context.Context, binary, home string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "CODEX_HOME=" + home}
	command.Dir = home
	command.WaitDelay = time.Second
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(output)), err
}

// execOptionBlocks recognizes the inspected CLI's full-help option declarations.
// Long flags share a column, while descriptions are indented further. Unknown
// layouts fail the preflight rather than promoting examples to capabilities.
func execOptionBlocks(help string) map[string]string {
	blocks := make(map[string]string)
	inOptions := false
	flagColumn := -1
	current := ""
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Options:" {
			inOptions = true
			current = ""
			continue
		}
		if !inOptions {
			continue
		}
		if line == trimmed && strings.HasSuffix(trimmed, ":") {
			inOptions = false
			current = ""
			continue
		}
		if match := execOptionDeclaration.FindStringSubmatch(line); match != nil {
			column := strings.Index(line, match[2])
			if flagColumn == -1 {
				flagColumn = column
			}
			if column == flagColumn {
				current = match[2]
				blocks[current] = ""
				continue
			}
		}
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent <= flagColumn {
			current = ""
		}
		if current != "" {
			blocks[current] += trimmed + "\n"
		}
	}
	return blocks
}

func optionAcceptsValue(block, value string) bool {
	for _, list := range execPossibleValues.FindAllStringSubmatch(block, -1) {
		for _, item := range strings.Split(list[1], ",") {
			if strings.TrimSpace(item) == value {
				return true
			}
		}
	}
	return false
}
