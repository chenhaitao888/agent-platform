package gitworkspace

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/backend/internal/repository"
)

func TestDiffReaderReadsTheRegisteredBaseAndHead(t *testing.T) {
	source, baseSHA, headSHA := createRepositoryFixture(t)
	root := t.TempDir()
	preparer, err := NewPreparer("git", root)
	if err != nil {
		t.Fatalf("create Git preparer: %v", err)
	}
	worktreePath := filepath.Join(root, "workspace-1", "worktree")
	if err := preparer.Prepare(context.Background(), Input{
		CloneURL:    (&url.URL{Scheme: "file", Path: source}).String(),
		BaseSHA:     baseSHA,
		HeadSHA:     headSHA,
		Destination: worktreePath,
	}); err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}

	reader, err := NewDiffReader("git", root)
	if err != nil {
		t.Fatalf("create Git diff reader: %v", err)
	}
	patch, err := reader.Read(context.Background(), repository.DiffInput{
		WorktreePath: worktreePath,
		BaseSHA:      baseSHA,
		HeadSHA:      headSHA,
	})
	if err != nil {
		t.Fatalf("read Git diff: %v", err)
	}

	if !strings.Contains(string(patch), "-base") || !strings.Contains(string(patch), "+head") {
		t.Fatalf("expected patch between the registered commits, got:\n%s", patch)
	}
}

func TestDiffReaderRejectsWorktreeOutsideConfiguredRootBeforeRunningGit(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "workspace-1", "worktree")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("create outside worktree: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "git-was-run")
	fakeGit := filepath.Join(t.TempDir(), "fake-git")
	writeExecutable(t, fakeGit, "#!/bin/sh\nprintf 'called' > "+shellQuote(marker)+"\nexit 0\n")
	reader, err := NewDiffReader(fakeGit, root)
	if err != nil {
		t.Fatalf("create Git diff reader: %v", err)
	}

	_, err = reader.Read(context.Background(), repository.DiffInput{
		WorktreePath: outside,
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if !errors.Is(err, repository.ErrDiffUnavailable) {
		t.Fatalf("expected unavailable diff for outside worktree, got %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git must not run for a worktree outside root, got %v", err)
	}
}

func TestDiffReaderRejectsSymlinkEscapingConfiguredRootBeforeRunningGit(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("create outside worktree: %v", err)
	}
	workspaceLink := filepath.Join(root, "workspace-1")
	if err := os.Symlink(filepath.Dir(outside), workspaceLink); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "git-was-run")
	fakeGit := filepath.Join(t.TempDir(), "fake-git")
	writeExecutable(t, fakeGit, "#!/bin/sh\nprintf 'called' > "+shellQuote(marker)+"\nexit 0\n")
	reader, err := NewDiffReader(fakeGit, root)
	if err != nil {
		t.Fatalf("create Git diff reader: %v", err)
	}

	_, err = reader.Read(context.Background(), repository.DiffInput{
		WorktreePath: filepath.Join(workspaceLink, "worktree"),
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if !errors.Is(err, repository.ErrDiffUnavailable) {
		t.Fatalf("expected unavailable diff for escaping symlink, got %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git must not run for a symlink escaping root, got %v", err)
	}
}

func TestDiffReaderRejectsOutputLargerThanTheLimit(t *testing.T) {
	testRoot := t.TempDir()
	fakeGit := filepath.Join(testRoot, "fake-git")
	writeExecutable(t, fakeGit, `#!/bin/sh
dd if=/dev/zero bs=1024 count=1025 2>/dev/null | tr '\0' x
`)
	worktreePath := filepath.Join(testRoot, "workspace-1", "worktree")
	if err := os.MkdirAll(worktreePath, 0o700); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	reader, err := NewDiffReader(fakeGit, testRoot)
	if err != nil {
		t.Fatalf("create Git diff reader: %v", err)
	}

	_, err = reader.Read(context.Background(), repository.DiffInput{
		WorktreePath: worktreePath,
		BaseSHA:      "1111111111111111111111111111111111111111",
		HeadSHA:      "2222222222222222222222222222222222222222",
	})
	if !errors.Is(err, repository.ErrDiffTooLarge) {
		t.Fatalf("expected an oversized diff error, got %v", err)
	}
}
