package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

const runnerExecHelp = `Options:
  -s, --sandbox <MODE>
          [possible values: read-only,workspace-write,danger-full-access]
      --ephemeral
      --output-schema <FILE>
  -o, --output-last-message <FILE>
      --json
      --ignore-user-config
      --ignore-rules
`

const runnerRootHelp = `Options:
      --no-daemon
  -a, --ask-for-approval <POLICY>
  -m, --model <MODEL>
`

type runnerCapture struct {
	Args   []string          `json:"args"`
	CWD    string            `json:"cwd"`
	Env    map[string]string `json:"env"`
	Stdin  string            `json:"stdin"`
	Schema json.RawMessage   `json:"schema"`
}

type runnerFixture struct {
	binary, root, worktree, capture string
}

func newRunnerFixture(t *testing.T, mode string, report []byte) runnerFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Python 3 is required for the external CLI fixture: ", err)
	}
	directory := t.TempDir()
	fixture := runnerFixture{
		binary:  filepath.Join(directory, "codex-fixture"),
		root:    filepath.Join(directory, "workspaces"),
		capture: filepath.Join(directory, "capture.json"),
	}
	fixture.worktree = filepath.Join(fixture.root, "workspace-1", "worktree")
	if err := os.MkdirAll(fixture.worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration, err := json.Marshal(map[string]string{
		"mode": mode, "report": string(report), "capture": fixture.capture,
		"execHelp": runnerExecHelp, "rootHelp": runnerRootHelp,
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "fixture.json")
	if err := os.WriteFile(configPath, configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "outside-output.json"), report, 0o600); err != nil {
		t.Fatal(err)
	}
	quotedPath, err := json.Marshal(configPath)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\n" + `import json, os, subprocess, sys, time
with open(` + string(quotedPath) + `) as source:
    config = json.load(source)
args = sys.argv[1:]
if args == ["--version"]:
    print("codex-cli 0.160.0")
elif args == ["exec", "--help"]:
    print(config["execHelp"])
elif args == ["--help"]:
    print(config["rootHelp"])
elif "sandbox" in args:
    if config["mode"] == "sandbox-fail": sys.exit(23)
else:
    schema = args[args.index("--output-schema") + 1]
    output = args[args.index("--output-last-message") + 1]
    with open(schema) as source:
        contract = json.load(source)
    capture = dict(args=args, cwd=os.getcwd(), env=dict(os.environ),
                   stdin=sys.stdin.read(), schema=contract)
    with open(config["capture"] + ".tmp", "w") as target:
        json.dump(capture, target)
    os.replace(config["capture"] + ".tmp", config["capture"])
    if config["mode"] == "hang":
        time.sleep(30)
    elif config["mode"] == "descendant":
        marker = os.path.join(os.path.dirname(sys.argv[0]), "escaped-child")
        subprocess.Popen([sys.executable, "-c",
                          "import sys, time; time.sleep(1.5); open(sys.argv[1], 'w').write('escaped')", marker],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with open(os.path.join(os.path.dirname(sys.argv[0]), "child-started"), "w") as target:
            target.write("started")
        time.sleep(30)
    elif config["mode"] == "missing":
        print(config["report"])
    elif config["mode"] == "symlink":
        os.symlink(os.path.join(os.path.dirname(sys.argv[0]), "outside-output.json"), output)
    elif config["mode"] == "hardlink":
        os.link(os.path.join(os.path.dirname(sys.argv[0]), "outside-output.json"), output)
    elif config["mode"] == "directory":
        os.mkdir(output)
    elif config["mode"] == "fifo":
        os.mkfifo(output)
    else:
        with open(output, "w") as target:
            target.write(config["report"])
        print('{"type":"turn.completed"}')
        if config["mode"] == "nonzero":
            print("diagnostic must not escape", file=sys.stderr)
            sys.exit(23)
`
	if err := os.WriteFile(fixture.binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func validRunnerReport(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile("../review/testdata/findings-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func (fixture runnerFixture) input() review.RunInput {
	return review.RunInput{
		WorktreePath: fixture.worktree,
		BaseSHA:      strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40),
		Patch: "diff --git a/src/review.go b/src/review.go\n+--dangerously-bypass-approvals-and-sandbox\n",
	}
}

func (fixture runnerFixture) runner(t *testing.T, timeout time.Duration) *codex.ExecRunner {
	t.Helper()
	runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{
		Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "review-model", Timeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func (fixture runnerFixture) readCapture(t *testing.T) runnerCapture {
	t.Helper()
	content, err := os.ReadFile(fixture.capture)
	if err != nil {
		t.Fatal(err)
	}
	var capture runnerCapture
	if err := json.Unmarshal(content, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func TestExecRunnerReturnsValidatedFindingsWithControlledProcessInput(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_GITLAB_TOKEN", "control-plane-sentinel")
	t.Setenv("CODEX_API_KEY", "personal-auth-sentinel")
	t.Setenv("OPENAI_API_KEY", "personal-openai-sentinel")
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	runner := fixture.runner(t, 5*time.Second)
	var port review.Runner = runner
	report, err := port.Run(context.Background(), fixture.input())
	if err != nil {
		t.Fatalf("run readonly review: %v", err)
	}
	if report.BaseSHA != fixture.input().BaseSHA || report.HeadSHA != fixture.input().HeadSHA || len(report.Findings) != 1 {
		t.Fatalf("unexpected validated findings: %+v", report)
	}
	capture := fixture.readCapture(t)
	home := capture.Env["HOME"]
	expectedArgs := []string{
		"--no-daemon", "--ask-for-approval", "never", "--model", "review-model", "exec",
		"--sandbox", "read-only", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--output-schema", filepath.Join(home, "findings-schema.json"),
		"--output-last-message", filepath.Join(home, "findings.json"), "--json", "-",
	}
	resolvedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capture.Args, expectedArgs) || capture.CWD != resolvedWorktree || home == "" || home == fixture.worktree {
		t.Fatalf("process must use fixed options, private output paths and the selected worktree: %+v", capture)
	}
	if capture.Env["AGENT_PLATFORM_GITLAB_TOKEN"] != "" || capture.Env["CODEX_API_KEY"] != "" || capture.Env["OPENAI_API_KEY"] != "" {
		t.Fatalf("control-plane and personal credentials reached the process: %+v", capture.Env)
	}
	if capture.Env["CODEX_HOME"] != filepath.Join(home, "codex") || capture.Env["TMPDIR"] != filepath.Join(home, "tmp") {
		t.Fatalf("configuration and temporary directories must belong to this execution: %+v", capture.Env)
	}
	var request struct {
		Instructions string `json:"instructions"`
		BaseSHA      string `json:"baseSha"`
		HeadSHA      string `json:"headSha"`
		Patch        string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(capture.Stdin), &request); err != nil {
		t.Fatal(err)
	}
	if request.Instructions == "" || request.BaseSHA != fixture.input().BaseSHA || request.HeadSHA != fixture.input().HeadSHA || request.Patch != fixture.input().Patch {
		t.Fatalf("fixed instructions and immutable input must be sent through stdin: %+v", request)
	}
	var actualSchema, expectedSchema any
	if err := json.Unmarshal(capture.Schema, &actualSchema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(review.FindingsSchema(), &expectedSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualSchema, expectedSchema) {
		t.Fatal("CLI did not receive the platform findings contract")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("execution directory must be removed after validation: %v", err)
	}
	if runner.Profile().Version != "0.160.0" || runner.Profile().BinarySHA256 == "" {
		t.Fatal("actual runtime identity must remain available for tracing")
	}
}

func TestExecRunnerRejectsOversizedOutput(t *testing.T) {
	fixture := newRunnerFixture(t, "normal", []byte(strings.Repeat(" ", (256<<10)+1)))
	report, err := fixture.runner(t, 5*time.Second).Run(context.Background(), fixture.input())
	if !errors.Is(err, review.ErrOutputTooLarge) || !reflect.DeepEqual(report, review.FindingsReport{}) {
		t.Fatalf("oversized output must have a bounded-output failure and no findings: %+v, %v", report, err)
	}
}

func TestExecRunnerRejectsLinkedOutput(t *testing.T) {
	for _, mode := range []string{"symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRunnerFixture(t, mode, validRunnerReport(t))
			report, err := fixture.runner(t, 5*time.Second).Run(context.Background(), fixture.input())
			if !errors.Is(err, review.ErrOutputUnavailable) || !reflect.DeepEqual(report, review.FindingsReport{}) {
				t.Fatalf("valid JSON outside the execution directory must not be read through a link: %+v, %v", report, err)
			}
		})
	}
}

func TestExecRunnerRejectsInvalidInputBeforeStartingReview(t *testing.T) {
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	runner := fixture.runner(t, 5*time.Second)
	outside := filepath.Join(fixture.root+"-extra", "workspace-1", "worktree")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(fixture.root, "workspace-2", "worktree")
	if err := os.Mkdir(filepath.Dir(linked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*review.RunInput)
	}{
		{"revision option injection", func(input *review.RunInput) { input.BaseSHA = "--model=unsafe" }},
		{"abbreviated head", func(input *review.RunInput) { input.HeadSHA = "2222222" }},
		{"uppercase revision", func(input *review.RunInput) { input.HeadSHA = strings.Repeat("A", 40) }},
		{"oversized patch", func(input *review.RunInput) { input.Patch = strings.Repeat("x", review.MaxReviewPatchBytes+1) }},
		{"non UTF-8 patch", func(input *review.RunInput) { input.Patch = string([]byte{0xff}) }},
		{"relative worktree", func(input *review.RunInput) { input.WorktreePath = "workspace-1/worktree" }},
		{"outside configured root", func(input *review.RunInput) { input.WorktreePath = outside }},
		{"symlink to outside root", func(input *review.RunInput) { input.WorktreePath = linked }},
		{"workspace parent instead of worktree", func(input *review.RunInput) { input.WorktreePath = filepath.Dir(fixture.worktree) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := fixture.input()
			test.change(&input)
			report, err := runner.Run(context.Background(), input)
			if !errors.Is(err, review.ErrInvalidRunInput) || !reflect.DeepEqual(report, review.FindingsReport{}) {
				t.Fatalf("invalid coordinates, content or path must fail before execution: %+v, %v", report, err)
			}
			if _, err := os.Stat(fixture.capture); !os.IsNotExist(err) {
				t.Fatalf("invalid input must not reach the review process: %v", err)
			}
		})
	}
}

func TestExecRunnerRejectsBinaryReplacementBeforeStartingReview(t *testing.T) {
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	runner := fixture.runner(t, 5*time.Second)
	script, err := os.ReadFile(fixture.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.binary, append(script, []byte("\n# changed deployment artifact\n")...), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), fixture.input())
	if !errors.Is(err, codex.ErrIncompatibleRuntime) || !reflect.DeepEqual(report, review.FindingsReport{}) {
		t.Fatalf("a replaced binary must be preflighted again before any review: %+v, %v", report, err)
	}
	if _, err := os.Stat(fixture.capture); !os.IsNotExist(err) {
		t.Fatalf("replaced binary must not run a review: %v", err)
	}
}

func TestExecRunnerClassifiesUnsuccessfulResults(t *testing.T) {
	valid := validRunnerReport(t)
	cases := []struct {
		name, mode string
		content    []byte
		want       error
	}{
		{"nonzero with valid output", "nonzero", valid, review.ErrExecutionFailed},
		{"stdout is not final output", "missing", valid, review.ErrOutputUnavailable},
		{"directory output", "directory", valid, review.ErrOutputUnavailable},
		{"fifo output", "fifo", valid, review.ErrOutputUnavailable},
		{"malformed JSON", "normal", []byte("not findings"), review.ErrInvalidFindings},
		{"different immutable head", "normal", []byte(strings.Replace(string(valid), strings.Repeat("2", 40), strings.Repeat("3", 40), 1)), review.ErrInvalidFindings},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, test.mode, test.content)
			report, err := fixture.runner(t, 5*time.Second).Run(context.Background(), fixture.input())
			if !errors.Is(err, test.want) || !reflect.DeepEqual(report, review.FindingsReport{}) {
				t.Fatalf("unsuccessful execution must return classified error and no findings: %+v, %v", report, err)
			}
			if strings.Contains(err.Error(), "diagnostic must not escape") {
				t.Fatal("runtime diagnostics escaped through the public error")
			}
			capture := fixture.readCapture(t)
			if _, err := os.Stat(capture.Env["HOME"]); !os.IsNotExist(err) {
				t.Fatalf("failed execution directory was not removed: %v", err)
			}
		})
	}
}

func TestExecRunnerHonorsExecutionTimeout(t *testing.T) {
	fixture := newRunnerFixture(t, "hang", validRunnerReport(t))
	report, err := fixture.runner(t, 2*time.Second).Run(context.Background(), fixture.input())
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(report, review.FindingsReport{}) {
		t.Fatalf("bounded execution must time out without findings: %+v, %v", report, err)
	}
	capture := fixture.readCapture(t)
	if _, err := os.Stat(capture.Env["HOME"]); !os.IsNotExist(err) {
		t.Fatalf("timed-out execution directory was not removed: %v", err)
	}
}

type runnerResult struct {
	report review.FindingsReport
	err    error
}

func waitForRunnerFile(t *testing.T, path string) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-timeout.C:
			t.Fatalf("external CLI did not create %s", filepath.Base(path))
		case <-poll.C:
			if _, err := os.Stat(path); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
}

func cancelRunnerAfterStart(t *testing.T, fixture runnerFixture, marker string) runnerResult {
	t.Helper()
	runner := fixture.runner(t, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan runnerResult, 1)
	go func() {
		report, err := runner.Run(ctx, fixture.input())
		done <- runnerResult{report: report, err: err}
	}()
	waitForRunnerFile(t, marker)
	cancel()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("caller cancellation did not stop the external CLI")
		return runnerResult{}
	}
}

func TestExecRunnerHonorsCancellationAfterProcessStart(t *testing.T) {
	fixture := newRunnerFixture(t, "hang", validRunnerReport(t))
	result := cancelRunnerAfterStart(t, fixture, fixture.capture)
	if !errors.Is(result.err, context.Canceled) || !reflect.DeepEqual(result.report, review.FindingsReport{}) {
		t.Fatalf("running review must stop without a partial result: %+v, %v", result.report, result.err)
	}
	capture := fixture.readCapture(t)
	if _, err := os.Stat(capture.Env["HOME"]); !os.IsNotExist(err) {
		t.Fatalf("cancelled execution directory was not removed: %v", err)
	}
}

func TestExecRunnerCancellationStopsDescendantProcesses(t *testing.T) {
	fixture := newRunnerFixture(t, "descendant", validRunnerReport(t))
	result := cancelRunnerAfterStart(t, fixture, filepath.Join(filepath.Dir(fixture.binary), "child-started"))
	if !errors.Is(result.err, context.Canceled) || !reflect.DeepEqual(result.report, review.FindingsReport{}) {
		t.Fatalf("cancelled review must have no findings: %+v, %v", result.report, result.err)
	}
	// The controlled child would write this marker 1.5 seconds after startup.
	// Waiting past that point checks the process-tree effect, not private calls.
	<-time.After(2 * time.Second)
	if _, err := os.Stat(filepath.Join(filepath.Dir(fixture.binary), "escaped-child")); !os.IsNotExist(err) {
		t.Fatalf("a descendant survived cancellation and wrote outside the execution: %v", err)
	}
}

func TestExecRunnerAcceptsEmptyAndMaximumSizeInputAndOutput(t *testing.T) {
	empty, err := os.ReadFile("../review/testdata/findings-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := validRunnerReport(t)
	maximum := append(append([]byte(nil), valid...), []byte(strings.Repeat(" ", review.MaxFindingsBytes-len(valid)))...)
	cases := []struct {
		name, patch string
		content     []byte
		count       int
	}{
		{"empty", "", empty, 0},
		{"maximum sizes", strings.Repeat("x", review.MaxReviewPatchBytes), maximum, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, "normal", test.content)
			input := fixture.input()
			input.Patch = test.patch
			report, err := fixture.runner(t, 5*time.Second).Run(context.Background(), input)
			if err != nil || len(report.Findings) != test.count {
				t.Fatalf("valid boundary input/output must be accepted: %+v, %v", report, err)
			}
		})
	}
}

func updateRunnerFixtureConfig(t *testing.T, fixture runnerFixture, key, value string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(fixture.binary), "fixture.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	config[key] = value
	content, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExecRunnerDoesNotReusePreviousOutputOrConfiguration(t *testing.T) {
	fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
	runner := fixture.runner(t, 5*time.Second)
	if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	first := fixture.readCapture(t)
	updateRunnerFixtureConfig(t, fixture, "mode", "missing")
	report, err := runner.Run(context.Background(), fixture.input())
	if !errors.Is(err, review.ErrOutputUnavailable) || !reflect.DeepEqual(report, review.FindingsReport{}) {
		t.Fatalf("another execution's findings must never be reused: %+v, %v", report, err)
	}
	second := fixture.readCapture(t)
	if first.Env["HOME"] == second.Env["HOME"] || first.Env["CODEX_HOME"] == second.Env["CODEX_HOME"] {
		t.Fatal("each execution must have new output and configuration directories")
	}
}

func TestExecRunnerRequiresDirectProcessCapabilities(t *testing.T) {
	for _, flag := range []string{"--no-daemon", "--ask-for-approval", "--model"} {
		t.Run(flag, func(t *testing.T) {
			fixture := newRunnerFixture(t, "normal", validRunnerReport(t))
			updateRunnerFixtureConfig(t, fixture, "rootHelp", strings.Replace(runnerRootHelp, flag, "", 1))
			runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{
				Binary: fixture.binary, WorkspaceRoot: fixture.root, Model: "review-model", Timeout: 5 * time.Second,
			})
			if runner != nil || !errors.Is(err, codex.ErrIncompatibleRuntime) {
				t.Fatalf("missing direct-exec capability must reject deployment before any review: %+v, %v", runner, err)
			}
			if _, err := os.Stat(fixture.capture); !os.IsNotExist(err) {
				t.Fatalf("startup preflight must not start a review: %v", err)
			}
		})
	}
}

func TestExecRunnerRejectsInvalidDeploymentConfiguration(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		root, model string
		timeout     time.Duration
	}{
		{"relative root", "workspaces", "review-model", time.Second},
		{"filesystem root", string(filepath.Separator), "review-model", time.Second},
		{"non-directory root", file, "review-model", time.Second},
		{"missing model", root, " ", time.Second},
		{"unbounded timeout", root, "review-model", 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runner, err := codex.NewExecRunner(context.Background(), codex.ExecConfig{
				Binary: "must-not-be-discovered", WorkspaceRoot: test.root, Model: test.model, Timeout: test.timeout,
			})
			if runner != nil || !errors.Is(err, codex.ErrInvalidRunnerConfig) {
				t.Fatalf("invalid deployment configuration must fail before binary discovery: %+v, %v", runner, err)
			}
		})
	}
}
