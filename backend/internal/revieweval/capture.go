package revieweval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
)

type Config struct {
	// The caller owns this empty private directory and cleans it up after Record.
	WorkspaceRoot string
	GitBinary     string
	Timeout       time.Duration
	Evidence      string
}

type ProfiledRunner interface {
	review.Runner
	review.RuntimeProfiler
}

// Record executes the embedded suite sequentially. Each case receives a fresh
// deadline covering preparation and inference. Cancellation retains completed
// evidence and marks all remaining cases skipped, rather than silently dropping
// them from the denominator. Raw diagnostics never enter the capture.
func Record(ctx context.Context, runner ProfiledRunner, config Config) (Capture, error) {
	if runner == nil || config.Timeout < time.Millisecond || config.Timeout > 2*time.Minute || (config.Evidence != "controlled" && config.Evidence != "deployment") || !filepath.IsAbs(config.WorkspaceRoot) {
		return Capture{}, ErrInvalidEvaluation
	}
	root, err := filepath.EvalSymlinks(config.WorkspaceRoot)
	if err != nil || root == "/" {
		return Capture{}, ErrInvalidEvaluation
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		return Capture{}, ErrInvalidEvaluation
	}
	git, err := exec.LookPath(config.GitBinary)
	if err != nil {
		return Capture{}, ErrInvalidEvaluation
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return Capture{}, ErrInvalidEvaluation
	}
	reader, err := gitworkspace.NewDiffReader(git, root)
	if err != nil {
		return Capture{}, ErrInvalidEvaluation
	}
	capture := Capture{SchemaVersion: "1.0", SuiteSHA256: SuiteSHA256(), Evidence: config.Evidence, StartedAt: time.Now().UTC(), CaseTimeoutMS: config.Timeout.Milliseconds(), Runtime: runner.RuntimeIdentity()}
	for i, c := range DefaultSuite().Cases {
		result := Result{CaseID: c.ID, State: "skipped"}
		if ctx.Err() == nil {
			caseCtx, cancel := context.WithTimeout(ctx, config.Timeout)
			started := time.Now()
			worktree := filepath.Join(root, fmt.Sprintf("workspace-%d", i+1), "worktree")
			input, prepErr := prepareCase(caseCtx, git, worktree, c, reader)
			result.BaseSHA, result.HeadSHA = input.BaseSHA, input.HeadSHA
			if prepErr != nil {
				result.State = "preparation_failed"
				if caseCtx.Err() != nil {
					result.State = resultState(caseCtx.Err())
				}
			} else {
				result.PatchSHA256 = digest([]byte(input.Patch))
				var report review.FindingsReport
				var runErr error
				if metered, ok := runner.(review.MeteredRunner); ok {
					report, result.Usage, runErr = metered.RunWithUsage(caseCtx, input)
				} else {
					report, runErr = runner.Run(caseCtx, input)
				}
				if ctxErr := caseCtx.Err(); ctxErr != nil {
					runErr = ctxErr
				}
				if runErr == nil {
					data, marshalErr := json.Marshal(report)
					if marshalErr != nil {
						runErr = review.ErrInvalidFindings
					} else {
						report, runErr = review.ParseFindings(data, input.BaseSHA, input.HeadSHA)
					}
				}
				result.State = resultState(runErr)
				if result.State == "invalid_output" {
					result.DiagnosticCode = review.OutputDiagnostic(runErr)
				}
				if runErr == nil {
					result.Report = &report
				}
			}
			result.DurationMS = time.Since(started).Milliseconds()
			cancel()
		}
		capture.Results = append(capture.Results, result)
	}
	return capture, nil
}

func resultState(err error) string {
	switch {
	case err == nil:
		return "succeeded"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, review.ErrInvalidFindings), errors.Is(err, review.ErrOutputTooLarge), errors.Is(err, review.ErrOutputUnavailable):
		return "invalid_output"
	default:
		return "execution_failed"
	}
}

func prepareCase(ctx context.Context, git, worktree string, c Case, reader *gitworkspace.DiffReader) (review.RunInput, error) {
	input := review.RunInput{WorktreePath: worktree}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		return input, err
	}
	run := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, git, append([]string{"-C", worktree, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgSign=false", "-c", "core.autocrlf=false"}, args...)...)
		// Deterministic author/time/object format, empty templates and no user
		// configuration: repeated captures produce identical Git object IDs.
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Dir(worktree), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "TZ=UTC",
			"GIT_AUTHOR_NAME=Evaluation", "GIT_AUTHOR_EMAIL=evaluation@example.invalid", "GIT_COMMITTER_NAME=Evaluation", "GIT_COMMITTER_EMAIL=evaluation@example.invalid",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00+0000", "GIT_COMMITTER_DATE=2026-01-01T00:00:00+0000"}
		command.WaitDelay = time.Second
		output, err := command.Output()
		return strings.TrimSpace(string(output)), err
	}
	if _, err := run("init", "--object-format=sha1", "--initial-branch=main", "--template="); err != nil {
		return input, err
	}
	for i, files := range []map[string]string{c.Base, c.Head} {
		for name, content := range files {
			// Only embedded, reviewed files can be materialized by this version.
			if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o600); err != nil {
				return input, err
			}
		}
		if _, err := run("add", "--all"); err != nil {
			return input, err
		}
		if _, err := run("commit", "--quiet", "--message", fmt.Sprintf("%s snapshot %d", c.ID, i)); err != nil {
			return input, err
		}
		sha, err := run("rev-parse", "HEAD")
		if err != nil || !validHex(sha, 40) {
			return input, ErrInvalidEvaluation
		}
		if i == 0 {
			input.BaseSHA = sha
		} else {
			input.HeadSHA = sha
		}
	}
	if _, err := run("checkout", "--detach", input.HeadSHA); err != nil {
		return input, err
	}
	patch, err := reader.Read(ctx, repository.DiffInput{WorktreePath: worktree, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA})
	input.Patch = string(patch)
	return input, err
}
