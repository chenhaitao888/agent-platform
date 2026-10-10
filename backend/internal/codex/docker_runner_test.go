package codex_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/review"
)

func TestDockerRunnerUsesFixedBwrapSeccompForProbeAndReview(t *testing.T) {
	fixture := newDockerFixture(t)
	config := fixture.config()
	config.SeccompPolicy = "codex-bwrap"
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	commands := fixture.commands(t)
	var digest string
	for _, index := range []int{1, 4} {
		capture := commands[index]
		if len(capture.Profile) == 0 {
			t.Fatal("container was not given an enforced seccomp profile")
		}
		actual := fmt.Sprintf("%x", sha256.Sum256(capture.Profile))
		if digest != "" && digest != actual {
			t.Fatal("preflight and review used different policies")
		}
		digest = actual
		var policy struct {
			DefaultAction string `json:"defaultAction"`
		}
		if err := json.Unmarshal(capture.Profile, &policy); err != nil || policy.DefaultAction != "SCMP_ACT_ERRNO" {
			t.Fatalf("profile does not deny unknown syscalls: %s", capture.Profile)
		}
		for _, arg := range capture.Args {
			if strings.HasPrefix(arg, "--security-opt=seccomp=") {
				path := strings.TrimPrefix(arg, "--security-opt=seccomp=")
				if !filepath.IsAbs(path) || path == "unconfined" {
					t.Fatalf("unsafe policy argument: %s", arg)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("temporary policy file was left behind")
				}
			}
		}
		if !containsArgument(capture.Args, "--cap-drop=ALL") || !containsArgument(capture.Args, "--security-opt=no-new-privileges") {
			t.Fatal("namespace compatibility widened process privileges")
		}
	}
	profile := runner.Profile()
	identity := runner.RuntimeIdentity()
	if profile.SeccompPolicy != "codex-bwrap" || profile.SeccompSHA256 != digest || identity.SeccompSHA256 != digest {
		t.Fatal("execution identity does not identify the applied seccomp policy")
	}
}

func TestDockerRunnerReturnsFindingsAndRemovesItsContainer(t *testing.T) {
	fixture := newDockerFixture(t)
	runner, err := codex.NewDockerRunner(context.Background(), fixture.config())
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if report.HeadSHA != fixture.input().HeadSHA || len(report.Findings) != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
	commands := fixture.commands(t)
	if len(commands) != 7 || commands[0].Args[0] != "image" || commands[6].Args[0] != "rm" {
		t.Fatalf("probe and run must each create/start/remove: %#v", commands)
	}
	create := commands[4]
	if runner.Profile().SeccompPolicy != codex.SeccompDockerDefault || runner.Profile().SeccompSHA256 != "" {
		t.Fatal("default deployment no longer delegates seccomp to Docker")
	}
	for _, index := range []int{1, 4} {
		for _, arg := range commands[index].Args {
			if strings.HasPrefix(arg, "--security-opt=seccomp=") {
				t.Fatal("default policy was silently replaced")
			}
		}
	}
	for _, flag := range []string{"--network=none", "--read-only", "--user=501:20", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pull=never", "--pids-limit=64", "--memory=256m", "--cpus=1"} {
		if !containsArgument(create.Args, flag) {
			t.Fatalf("missing boundary %s: %v", flag, create.Args)
		}
	}
	if !containsArgument(create.Args, "--log-driver=none") {
		t.Fatal("daemon log retention was enabled")
	}
	mount := "type=bind,source=" + filepath.Dir(fixture.worktree) + ",target=" + filepath.Dir(fixture.worktree) + ",readonly"
	if !containsArgument(create.Args, mount) {
		t.Fatalf("expected only this workspace: %v", create.Args)
	}
	if create.Env["DOCKER_HOST"] != fixture.config().Endpoint || create.Env["OPENAI_API_KEY"] != "" || create.Env["DOCKER_CONTEXT"] != "" {
		t.Fatalf("unexpected client environment: %#v", create.Env)
	}
	var input review.RunInput
	if err := json.Unmarshal([]byte(commands[5].Stdin), &input); err != nil || input != fixture.input() {
		t.Fatalf("request changed: %#v, %v", input, err)
	}
	if commands[3].Args[2] == commands[6].Args[2] {
		t.Fatal("probe and run reused a container")
	}
}

func TestDockerRunnerRejectsFailedOrUntrustedOutputAndStillCleansUp(t *testing.T) {
	for _, scenario := range []struct {
		mode string
		want error
	}{
		{"nonzero", review.ErrExecutionFailed}, {"oversize", review.ErrOutputTooLarge}, {"invalid", review.ErrInvalidFindings},
		{"mismatch", review.ErrInvalidFindings}, {"worker-error", review.ErrOutputUnavailable},
	} {
		t.Run(scenario.mode, func(t *testing.T) {
			fixture := newDockerFixture(t)
			runner, err := codex.NewDockerRunner(context.Background(), fixture.config())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte(scenario.mode), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(context.Background(), fixture.input())
			if !errors.Is(err, scenario.want) || report.SchemaVersion != "" {
				t.Fatalf("expected no report and %v, got %#v, %v", scenario.want, report, err)
			}
			commands := fixture.commands(t)
			if commands[len(commands)-1].Args[0] != "rm" {
				t.Fatal("missing cleanup")
			}
			entries, err := filepath.Glob(filepath.Join(fixture.directory, "agent-platform-review-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("container left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestDockerRunnerCancellationAndTimeoutRemoveOwnedContainerAndPolicy(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			fixture := newDockerFixture(t)
			config := fixture.config()
			config.Timeout = 700 * time.Millisecond
			config.SeccompPolicy = codex.SeccompCodexBwrap
			runner, err := codex.NewDockerRunner(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.directory, "mode"), []byte("hang"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				time.AfterFunc(300*time.Millisecond, cancel)
			}
			_, err = runner.Run(ctx, fixture.input())
			want := context.DeadlineExceeded
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			commands := fixture.commands(t)
			if len(commands) != 7 || commands[6].Args[0] != "rm" {
				t.Fatalf("missing independent cleanup: %#v", commands)
			}
			for _, arg := range commands[4].Args {
				if strings.HasPrefix(arg, "--security-opt=seccomp=") {
					path := strings.TrimPrefix(arg, "--security-opt=seccomp=")
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("canceled/timed out execution left its policy file behind")
					}
				}
			}
		})
	}
}

func TestDockerRunnerDoesNotPublishFindingsIfCleanupFails(t *testing.T) {
	fixture := newDockerFixture(t)
	runner, err := codex.NewDockerRunner(context.Background(), fixture.config())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.directory, "fail-cleanup"), []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), fixture.input())
	if !errors.Is(err, review.ErrExecutionFailed) || report.SchemaVersion != "" {
		t.Fatalf("cleanup failure published report: %#v %v", report, err)
	}
}

func TestDockerRunnerRejectsImageWithoutVerifiedNativeSandbox(t *testing.T) {
	fixture := newDockerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.directory, "unverified-sandbox"), []byte("false"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.NewDockerRunner(context.Background(), fixture.config()); !errors.Is(err, codex.ErrIncompatibleRuntime) {
		t.Fatalf("unverified sandbox accepted: %v", err)
	}
	commands := fixture.commands(t)
	if len(commands) != 4 || commands[3].Args[0] != "rm" {
		t.Fatal("failed preflight did not remove its probe container")
	}
}

func TestDockerRunnerRejectsUnsafeDeploymentAndWorkspaceBeforeDocker(t *testing.T) {
	fixture := newDockerFixture(t)
	for _, change := range []func(*codex.DockerConfig){
		func(c *codex.DockerConfig) { c.SeccompPolicy = "unconfined" },
		func(c *codex.DockerConfig) { c.SeccompPolicy = "/tmp/custom-policy.json" },
		func(c *codex.DockerConfig) { c.Image = "codex:latest" },
		func(c *codex.DockerConfig) { c.UID = 0 },
		func(c *codex.DockerConfig) { c.Endpoint = "tcp://remote:2375" },
		func(c *codex.DockerConfig) { c.WorkspaceRoot = "/" },
	} {
		config := fixture.config()
		change(&config)
		if _, err := codex.NewDockerRunner(context.Background(), config); !errors.Is(err, codex.ErrInvalidRunnerConfig) {
			t.Fatalf("unsafe config accepted: %#v %v", config, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.directory, "commands.jsonl")); !os.IsNotExist(err) {
		t.Fatal("unsafe deployment reached Docker")
	}
	runner, err := codex.NewDockerRunner(context.Background(), fixture.config())
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.WorktreePath = t.TempDir()
	if _, err := runner.Run(context.Background(), input); !errors.Is(err, review.ErrInvalidRunInput) {
		t.Fatal(err)
	}
	if len(fixture.commands(t)) != 4 {
		t.Fatal("invalid input launched container")
	}
	input = fixture.input()
	input.WorktreePath = filepath.Join(fixture.root, "workspace-2,target=other", "worktree")
	if err := os.MkdirAll(input.WorktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), input); !errors.Is(err, review.ErrInvalidRunInput) {
		t.Fatalf("CSV mount delimiter reached Docker: %v", err)
	}
	if len(fixture.commands(t)) != 4 {
		t.Fatal("unsafe mount reached Docker")
	}
	profile := runner.Profile()
	profile.CLI.RequiredFlags[0] = "changed"
	if runner.Profile().CLI.RequiredFlags[0] == "changed" {
		t.Fatal("profile exposes mutable deployment identity")
	}
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

type dockerCapture struct {
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Stdin   string            `json:"stdin"`
	Profile []byte            `json:"profile"`
}
type dockerFixture struct{ directory, binary, root, worktree string }

func newDockerFixture(t *testing.T) dockerFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	fixture := dockerFixture{directory: directory, binary: filepath.Join(directory, "docker-fixture"), root: filepath.Join(directory, "workspaces")}
	fixture.worktree = filepath.Join(fixture.root, "workspace-1", "worktree")
	if err := os.MkdirAll(fixture.worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.root, err = filepath.EvalSymlinks(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.worktree = filepath.Join(fixture.root, "workspace-1", "worktree")
	quoted, _ := json.Marshal(directory)
	script := "#!" + python + "\n" + `import base64, json, os, sys, time
directory = ` + string(quoted) + `
args = sys.argv[1:]
body = sys.stdin.read()
profile = None
for arg in args:
    if arg.startswith("--security-opt=seccomp="):
        with open(arg.split("=",2)[2]) as source: profile = source.read()
with open(directory+"/commands.jsonl","a") as target:
    target.write(json.dumps(dict(args=args,env=dict(os.environ),stdin=body,profile=base64.b64encode(profile.encode()).decode() if profile else None))+"\n")
if args[0] == "image":
    print(json.dumps([dict(Id="sha256:"+"a"*64,Os="linux",Architecture="arm64")]))
elif args[0] == "create":
    name = args[args.index("--name")+1]
    with open(directory+"/"+name,"w") as target: json.dump(args,target)
    print("b"*64)
elif args[0] == "start":
    with open(directory+"/"+args[-1]) as source: created = json.load(source)
    if "--probe" in created:
        print(json.dumps(dict(sandboxVerified=not os.path.exists(directory+"/unverified-sandbox"),profile=dict(version="0.999.0",binaryPath="/opt/codex/codex",binarySha256="c"*64,integration="exec",sandbox="read-only",ephemeral=True,requiredFlags=["--sandbox","--ephemeral","--output-schema","--output-last-message","--json","--ignore-user-config","--ignore-rules","--no-daemon","--ask-for-approval","--model"]))))
    else:
        if os.path.exists(directory+"/mode"):
            with open(directory+"/mode") as source: mode=source.read()
        else: mode="success"
        if mode == "hang": time.sleep(30)
        request=json.loads(body)
        if mode == "nonzero": sys.exit(23)
        if mode == "oversize": print("x"*300000)
        elif mode == "invalid": print('{"report":{}}')
        elif mode == "mismatch": print(json.dumps(dict(report=dict(schemaVersion="1.0",baseSha="d"*40,headSha=request["headSha"],findings=[]))))
        elif mode == "worker-error": print('{"errorCode":"review_output_unavailable"}')
        else: print(json.dumps(dict(report=dict(schemaVersion="1.0",baseSha=request["baseSha"],headSha=request["headSha"],findings=[]))))
elif args[0] == "rm":
    if os.path.exists(directory+"/fail-cleanup"): sys.exit(24)
    os.remove(directory+"/"+args[-1])
`
	if err := os.WriteFile(fixture.binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture
}
func (fixture dockerFixture) config() codex.DockerConfig {
	return codex.DockerConfig{Binary: fixture.binary, Endpoint: "unix:///tmp/review-docker.sock", Image: "sha256:" + strings.Repeat("a", 64), WorkspaceRoot: fixture.root, Model: "deployment-review-model", Timeout: 2 * time.Second, UID: 501, GID: 20}
}
func (fixture dockerFixture) input() review.RunInput {
	return review.RunInput{WorktreePath: fixture.worktree, BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), Patch: "diff --git a/a b/a\n+untrusted --network=host\n"}
}
func (fixture dockerFixture) commands(t *testing.T) []dockerCapture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.directory, "commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var captures []dockerCapture
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var capture dockerCapture
		if err := json.Unmarshal([]byte(line), &capture); err != nil {
			t.Fatal(err)
		}
		captures = append(captures, capture)
	}
	return captures
}
