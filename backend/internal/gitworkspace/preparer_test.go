package gitworkspace

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparerClonesRepositoryAndChecksOutImmutableHead(t *testing.T) {
	source, baseSHA, headSHA := createRepositoryFixture(t)
	preparer, err := NewPreparer("git")
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "workspace-1", "worktree")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()

	err = preparer.Prepare(context.Background(), Input{
		CloneURL:    sourceURL,
		BaseSHA:     baseSHA,
		HeadSHA:     headSHA,
		Destination: destination,
	})
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}

	if actual := gitOutput(t, "-C", destination, "rev-parse", "HEAD"); actual != headSHA {
		t.Fatalf("expected worktree HEAD %q, got %q", headSHA, actual)
	}
	gitRun(t, "-C", destination, "cat-file", "-e", baseSHA+"^{commit}")
	if actual := gitOutput(t, "-C", destination, "rev-parse", "--abbrev-ref", "HEAD"); actual != "HEAD" {
		t.Fatalf("expected detached HEAD, got %q", actual)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "git-askpass.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temporary askpass helper to be removed, got %v", err)
	}
}

func TestPreparerPassesPasswordThroughEnvironmentInsteadOfArguments(t *testing.T) {
	testRoot := t.TempDir()
	argumentLog := filepath.Join(testRoot, "arguments.txt")
	passwordLog := filepath.Join(testRoot, "password.txt")
	inheritedTokenLog := filepath.Join(testRoot, "inherited-token.txt")
	t.Setenv("AGENT_PLATFORM_GITLAB_TOKEN", "outer-process-token")
	fakeGit := filepath.Join(testRoot, "fake-git")
	writeExecutable(t, fakeGit, `#!/bin/sh
directory=$(dirname "$0")
printf '%s\n' "$@" > "$directory/arguments.txt"
printf '%s\n' "$AGENT_PLATFORM_GIT_PASSWORD" > "$directory/password.txt"
printf '%s\n' "$AGENT_PLATFORM_GITLAB_TOKEN" > "$directory/inherited-token.txt"
exit 1
`)
	preparer, err := NewPreparer(fakeGit)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	secret := "token-that-must-not-appear-in-argv"
	destination := filepath.Join(testRoot, "workspace-1", "worktree")

	err = preparer.Prepare(context.Background(), Input{
		CloneURL:    "https://gitlab.example.com/platform/agent-project.git",
		BaseSHA:     "1111111111111111111111111111111111111111",
		HeadSHA:     "2222222222222222222222222222222222222222",
		Destination: destination,
		Credentials: Credentials{Username: "oauth2", Password: secret},
	})
	if !errors.Is(err, ErrGitOperation) {
		t.Fatalf("expected Git operation failure, got %v", err)
	}
	arguments, err := os.ReadFile(argumentLog)
	if err != nil {
		t.Fatalf("read captured arguments: %v", err)
	}
	if strings.Contains(string(arguments), secret) {
		t.Fatalf("secret leaked into Git arguments: %s", arguments)
	}
	password, err := os.ReadFile(passwordLog)
	if err != nil {
		t.Fatalf("read captured password: %v", err)
	}
	if strings.TrimSpace(string(password)) != secret {
		t.Fatal("expected the controlled Git process to receive the password through its environment")
	}
	inheritedToken, err := os.ReadFile(inheritedTokenLog)
	if err != nil {
		t.Fatalf("read captured inherited token: %v", err)
	}
	if strings.TrimSpace(string(inheritedToken)) != "" {
		t.Fatal("expected the Git process environment to exclude the control-plane GitLab token")
	}
	if _, err := os.Stat(filepath.Dir(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed workspace directory to be removed, got %v", err)
	}
}

func createRepositoryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	working := filepath.Join(t.TempDir(), "working")
	gitRun(t, "init", working)
	gitRun(t, "-C", working, "config", "user.name", "Agent Platform Test")
	gitRun(t, "-C", working, "config", "user.email", "agent-platform@example.invalid")
	writeFixtureFile(t, filepath.Join(working, "README.md"), "base\n")
	gitRun(t, "-C", working, "add", "README.md")
	gitRun(t, "-C", working, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	baseSHA := gitOutput(t, "-C", working, "rev-parse", "HEAD")

	writeFixtureFile(t, filepath.Join(working, "README.md"), "head\n")
	gitRun(t, "-C", working, "add", "README.md")
	gitRun(t, "-C", working, "-c", "commit.gpgsign=false", "commit", "-m", "head")
	headSHA := gitOutput(t, "-C", working, "rev-parse", "HEAD")

	bare := filepath.Join(t.TempDir(), "source.git")
	gitRun(t, "clone", "--bare", working, bare)
	return bare, baseSHA, headSHA
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func gitRun(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
