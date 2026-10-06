package codex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const compatibleExecHelp = `Run Codex non-interactively

Usage: codex exec [OPTIONS] [PROMPT]

Options:
  -s, --sandbox <SANDBOX_MODE>
          Select the sandbox policy
          [possible values: read-only, workspace-write, danger-full-access]

      --ephemeral
          Run without persisting session files

      --output-schema <FILE>
          JSON Schema for the final response

  -o, --output-last-message <FILE>
          Write the final message to a file

      --json
          Print events as JSONL

      --ignore-user-config
          Ignore user configuration

      --ignore-rules
          Ignore user and project rules
`

func TestProbeAcceptsCompatibleVersionsWithoutStartingAReview(t *testing.T) {
	for _, version := range []string{"0.154.0", "0.160.0"} {
		t.Run(version, func(t *testing.T) {
			binary, digest := probeFixture(t, "codex-cli "+version, compatibleExecHelp)
			profile, err := Probe(context.Background(), binary, digest)
			if err != nil {
				t.Fatalf("probe compatible CLI: %v", err)
			}
			if profile.Version != version || profile.BinaryPath != binary || profile.BinarySHA256 != digest {
				t.Fatalf("unexpected verified CLI identity: %+v", profile)
			}
		})
	}
}

func TestProbeRecognizesSandboxValuesInCLIHelp(t *testing.T) {
	binary, digest := probeFixture(t, "codex-cli 0.154.0", compatibleExecHelp)
	if _, err := Probe(context.Background(), binary, digest); err != nil {
		t.Fatalf("the actual CLI help format must be supported: %v", err)
	}
}

func TestProbeRecordsChecksumWhenNoPinIsConfigured(t *testing.T) {
	binary, digest := probeFixture(t, "codex-cli 0.160.0", compatibleExecHelp)
	profile, err := Probe(context.Background(), binary, "")
	if err != nil {
		t.Fatalf("a checksum pin is optional for capability inspection: %v", err)
	}
	if profile.Version != "0.160.0" || profile.BinaryPath != binary || profile.BinarySHA256 != digest {
		t.Fatalf("actual runtime identity must be recorded without a pin: %+v", profile)
	}
}

func TestProbeRejectsFlagsMentionedOnlyInDescriptions(t *testing.T) {
	withoutJSON := strings.Replace(compatibleExecHelp, "      --json\n", "", 1)
	cases := []struct{ name, help string }{
		{"description", strings.Replace(compatibleExecHelp, "      --json\n", "          An example mentions --json without declaring it\n", 1)},
		{"indented example", withoutJSON + "          --json\n"},
		{"another section", withoutJSON + "Examples:\n      --json\n"},
		{"introductory text", "This example mentions --json without declaring it\n" + withoutJSON},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binary, digest := probeFixture(t, "codex-cli 0.160.0", test.help)
			profile, err := Probe(context.Background(), binary, digest)
			if !errors.Is(err, ErrIncompatibleRuntime) || profile.Version != "" {
				t.Fatalf("text outside an option declaration must not establish a flag: %+v, %v", profile, err)
			}
		})
	}
}

func TestProbeRequiresReadOnlyInSandboxPossibleValues(t *testing.T) {
	unsupported := strings.Replace(compatibleExecHelp, "read-only, ", "", 1)
	cases := []struct{ name, help string }{
		{"another option's values", unsupported + `
      --color <COLOR>
          [possible values: read-only, always, never]
`},
		{"introductory text", "Run in read-only mode\n" + unsupported},
		{"sandbox description", strings.Replace(unsupported, "Select the sandbox policy", "Select a read-only sandbox policy", 1)},
		{"different enum value", strings.Replace(compatibleExecHelp, "read-only, ", "read-only-extra, ", 1)},
		{"unterminated enum", strings.Replace(compatibleExecHelp, "danger-full-access]", "danger-full-access", 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binary, digest := probeFixture(t, "codex-cli 0.160.0", test.help)
			profile, err := Probe(context.Background(), binary, digest)
			if !errors.Is(err, ErrIncompatibleRuntime) || profile.Version != "" {
				t.Fatalf("read-only must be an exact sandbox enum value: %+v, %v", profile, err)
			}
		})
	}
}

func TestProbeAcceptsCompactAndWrappedSandboxValues(t *testing.T) {
	for _, values := range []string{
		"[possible values: read-only,workspace-write,danger-full-access]",
		"[possible values:\n          read-only,\n          workspace-write, danger-full-access]",
	} {
		t.Run(values, func(t *testing.T) {
			help := strings.Replace(compatibleExecHelp, "[possible values: read-only, workspace-write, danger-full-access]", values, 1)
			binary, digest := probeFixture(t, "codex-cli 0.160.0", help)
			if _, err := Probe(context.Background(), binary, digest); err != nil {
				t.Fatalf("sandbox enum formatting must not reject a compatible CLI: %v", err)
			}
		})
	}
}

func probeFixture(t *testing.T, version, help string) (string, string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n--version) printf '%%s\\n' %s;;\n'exec --help') printf '%%s\\n' %s;;\n*) exit 1;;\nesac\n", probeShellQuote(version), probeShellQuote(help))
	return probeScriptFixture(t, script)
}

func probeShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func probeScriptFixture(t *testing.T, script string) (string, string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
}

func TestProbeRejectsWrongIdentityAndMissingCapabilities(t *testing.T) {
	const help = compatibleExecHelp
	cases := []struct{ name, version, help string }{
		{"wrong CLI identity", "another-cli 0.160.0", help},
		{"missing version", "codex-cli", help},
		{"unexpected version output", "codex-cli 0.160.0 unexpected", help},
		{"unsupported sandbox", "codex-cli 0.154.0", strings.Replace(help, "read-only", "workspace-write", 1)},
	}
	for _, flag := range []string{"--sandbox", "--ephemeral", "--output-schema", "--output-last-message", "--json", "--ignore-user-config", "--ignore-rules"} {
		cases = append(cases, struct{ name, version, help string }{"missing " + flag, "codex-cli 0.154.0", strings.Replace(help, flag, "", 1)})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binary, digest := probeFixture(t, test.version, test.help)
			profile, err := Probe(context.Background(), binary, digest)
			if !errors.Is(err, ErrIncompatibleRuntime) || profile.Version != "" {
				t.Fatalf("incompatible CLI must return no verified profile: %+v, %v", profile, err)
			}
		})
	}
}

func TestProbeRejectsChecksumMismatchBeforeExecutingTheBinary(t *testing.T) {
	binary, _ := probeFixture(t, "codex-cli 0.154.0", "unused")
	if err := os.WriteFile(binary, []byte("this binary must not be executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"invalid", strings.Repeat("0", 64)} {
		t.Run(fmt.Sprintf("%q", digest), func(t *testing.T) {
			if _, err := Probe(context.Background(), binary, digest); !errors.Is(err, ErrIncompatibleRuntime) {
				t.Fatalf("reject invalid/mismatched digest before executable format is examined: %v", err)
			}
		})
	}
}

func TestProbeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, "not-an-existing-binary", strings.Repeat("0", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe must not start binary discovery or execution: %v", err)
	}
}

func TestProbeHonorsCancellationDuringHelp(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "help-started")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  --version) printf 'codex-cli 0.160.0\n' ;;
  'exec --help') printf 'started' > %s; exec /bin/sleep 30 ;;
  *) exit 1 ;;
esac
`, probeShellQuote(marker))
	binary, digest := probeScriptFixture(t, script)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		profile Profile
		err     error
	}
	done := make(chan result, 1)
	go func() {
		profile, err := Probe(ctx, binary, digest)
		done <- result{profile: profile, err: err}
	}()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
waitForHelp:
	for {
		select {
		case received := <-done:
			t.Fatalf("probe returned before help started: %+v, %v", received.profile, received.err)
		case <-timeout.C:
			t.Fatal("help command did not start within the test deadline")
		case <-poll.C:
			if _, err := os.Stat(marker); err == nil {
				break waitForHelp
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	cancel()
	select {
	case received := <-done:
		if !errors.Is(received.err, context.Canceled) || received.profile.Version != "" {
			t.Fatalf("cancelling the caller must stop help without a verified profile: %+v, %v", received.profile, received.err)
		}
	case <-timeout.C:
		t.Fatal("caller cancellation did not stop help promptly")
	}
}

func TestProbeBoundsCombinedInspectionTime(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "help-started")
	// Each command is below ten seconds; their combined duration exceeds the
	// inspection budget. exec replaces the shell so cancellation kills sleep.
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  --version) printf 'codex-cli 0.160.0\n'; exec /bin/sleep 4 ;;
  'exec --help')
    printf 'started' > %s
    cat <<'HELP'
%sHELP
    exec /bin/sleep 7 ;;
  *) exit 1 ;;
esac
`, probeShellQuote(marker), compatibleExecHelp)
	binary, digest := probeScriptFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	profile, err := Probe(ctx, binary, digest)
	if !errors.Is(err, context.DeadlineExceeded) || profile.Version != "" || ctx.Err() != nil {
		t.Fatalf("version and help must share a shorter inspection deadline: %+v, %v, caller=%v", profile, err, ctx.Err())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("version must finish and help must start before the inspection deadline: %v", err)
	}
}

func TestProbeIsolatesConfigurationAndControlPlaneCredentials(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_GITLAB_TOKEN", "must-not-reach-probe")
	t.Setenv("CODEX_API_KEY", "must-not-reach-probe")
	script := `#!/bin/sh
test -z "$AGENT_PLATFORM_GITLAB_TOKEN" || exit 1
test -z "$CODEX_API_KEY" || exit 1
test -d "$CODEX_HOME" || exit 1
test ! -e "$CODEX_HOME/config.toml" || exit 1
test ! -e "$CODEX_HOME/auth.json" || exit 1
case "$*" in
  --version) printf 'codex-cli 0.154.0\n' ;;
  'exec --help') cat <<'HELP'
` + compatibleExecHelp + `HELP
    ;;
  *) exit 1 ;;
esac
`
	binary, digest := probeScriptFixture(t, script)
	if _, err := Probe(context.Background(), binary, digest); err != nil {
		t.Fatalf("probe must use fresh configuration and exclude control-plane credentials: %v", err)
	}
}
