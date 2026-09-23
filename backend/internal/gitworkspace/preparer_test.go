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
	root := t.TempDir()
	preparer, err := NewPreparer("git", root)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	destination := filepath.Join(root, "workspace-1", "worktree")
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

func TestPreparerRejectsDestinationOutsideConfiguredRootBeforeRunningGit(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspaces")
	sibling := filepath.Join(base, "workspaces-extra")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create configured root: %v", err)
	}
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatalf("create sibling directory: %v", err)
	}
	outside := filepath.Join(sibling, "workspace-1", "worktree")
	marker := filepath.Join(t.TempDir(), "git-was-run")
	fakeGit := filepath.Join(t.TempDir(), "fake-git")
	writeExecutable(t, fakeGit, "#!/bin/sh\nprintf 'called' > "+shellQuote(marker)+"\nexit 1\n")
	preparer, err := NewPreparer(fakeGit, root)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}

	err = preparer.Prepare(context.Background(), Input{
		CloneURL:    "https://gitlab.example.com/platform/project.git",
		BaseSHA:     "1111111111111111111111111111111111111111",
		HeadSHA:     "2222222222222222222222222222222222222222",
		Destination: outside,
	})
	if !errors.Is(err, ErrGitOperation) {
		t.Fatalf("expected invalid destination error, got %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git must not run for a destination outside root, got %v", err)
	}
}

func TestPreparerSkipsFetchWhenBothCommitsAlreadyCloned(t *testing.T) {
	source, baseSHA, headSHA := createRepositoryFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git executable: %v", err)
	}
	// 真实 Git 负责 clone 和检出；包装脚本只在发生 fetch 时拒绝命令。
	// 因此测试的是完整的 Prepare 行为，而不是某个内部函数的调用次数。
	gitWrapper := filepath.Join(t.TempDir(), "git-without-fetch")
	writeExecutable(t, gitWrapper, `#!/bin/sh
for argument in "$@"; do
  if [ "$argument" = fetch ]; then
    exit 91
  fi
done
exec `+shellQuote(realGit)+` "$@"
`)
	workspaceRoot := t.TempDir()
	preparer, err := NewPreparer(gitWrapper, workspaceRoot)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	destination := filepath.Join(workspaceRoot, "workspace-1", "worktree")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()

	if err := preparer.Prepare(context.Background(), Input{
		CloneURL:    sourceURL,
		BaseSHA:     baseSHA,
		HeadSHA:     headSHA,
		Destination: destination,
	}); err != nil {
		t.Fatalf("clone already contains both commits; fetch must not run: %v", err)
	}
	if actual := gitOutput(t, "-C", destination, "rev-parse", "HEAD"); actual != headSHA {
		t.Fatalf("expected worktree HEAD %q, got %q", headSHA, actual)
	}
}

func TestPreparerFetchesOnlyMissingCommitWithCredentials(t *testing.T) {
	source, baseSHA, _ := createRepositoryFixture(t)
	missingHeadSHA := strings.Repeat("f", 40)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git executable: %v", err)
	}
	testRoot := t.TempDir()
	fetchLog := filepath.Join(testRoot, "fetch-arguments.txt")
	gitWrapper := filepath.Join(testRoot, "git-wrapper")
	writeExecutable(t, gitWrapper, `#!/bin/sh
for argument in "$@"; do
  if [ "$argument" = cat-file ]; then
    if [ -n "$AGENT_PLATFORM_GIT_PASSWORD" ] || [ -n "$GIT_ASKPASS" ]; then
      exit 92
    fi
  fi
  if [ "$argument" = fetch ]; then
    if [ -z "$AGENT_PLATFORM_GIT_PASSWORD" ] || [ ! -x "$GIT_ASKPASS" ]; then
      exit 93
    fi
    printf '%s\n' "$@" > `+shellQuote(fetchLog)+`
    exit 94
  fi
done
exec `+shellQuote(realGit)+` "$@"
`)
	preparer, err := NewPreparer(gitWrapper, testRoot)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	destination := filepath.Join(testRoot, "workspace-1", "worktree")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()

	prepareErr := preparer.Prepare(context.Background(), Input{
		CloneURL:    sourceURL,
		BaseSHA:     baseSHA,
		HeadSHA:     missingHeadSHA,
		Destination: destination,
		Credentials: Credentials{Username: "oauth2", Password: "test-token"},
	})
	if !errors.Is(prepareErr, ErrGitOperation) {
		t.Fatalf("expected fetch failure, got %v", prepareErr)
	}
	fetchArguments, err := os.ReadFile(fetchLog)
	if err != nil {
		t.Fatalf("fetch was not attempted with credentials: %v (prepare error: %v)", err, prepareErr)
	}
	if !strings.Contains(string(fetchArguments), missingHeadSHA) || strings.Contains(string(fetchArguments), baseSHA) {
		t.Fatalf("fetch must request only the missing head commit, got %s", fetchArguments)
	}
	if _, err := os.Stat(filepath.Dir(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed workspace directory to be removed, got %v", err)
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
	preparer, err := NewPreparer(fakeGit, testRoot)
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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
