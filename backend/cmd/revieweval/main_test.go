package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/revieweval"
)

func TestExtendedSuiteCanBeSelectedWithoutChangingTheDefault(t *testing.T) {
	for _, choice := range []struct {
		name  string
		cases int
	}{{"starter-v1", 6}, {"extended-v1", 12}} {
		output := filepath.Join(t.TempDir(), "suite.json")
		if err := run(context.Background(), []string{"-mode", "suite", "-suite", choice.name, "-out", output}); err != nil {
			t.Fatal(err)
		}
		var suite revieweval.Suite
		readTestJSON(t, output, &suite)
		if len(suite.Cases) != choice.cases {
			t.Fatalf("%s: got %d cases", choice.name, len(suite.Cases))
		}
	}
	for _, args := range [][]string{
		{"-mode", "capture", "-suite", "../untrusted.json", "-docker", "/must-not-run"},
		{"-mode", "suite", "-suite", "future-version"},
		{"-mode", "score", "-suite", "extended-v1"},
	} {
		output := filepath.Join(t.TempDir(), "rejected.json")
		err := run(context.Background(), append(args, "-out", output))
		if err == nil || !strings.Contains(err.Error(), "score selects only by capture digest") {
			t.Fatal("suite selection was not rejected before execution", err)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("invalid suite reserved an output or started capture")
		}
	}
}

func TestExecutableOfflineScoreAndOutputProtection(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "revieweval")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	run := func(wantSuccess bool, args ...string) {
		t.Helper()
		// No Docker, model credentials or personal configuration for scoring.
		command := exec.Command(binary, args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory}
		output, err := command.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("command: %v %s", err, output)
		}
	}
	suitePath := filepath.Join(directory, "suite.json")
	run(true, "-mode", "suite", "-out", suitePath)
	var suite revieweval.Suite
	readTestJSON(t, suitePath, &suite)
	if len(suite.Cases) != 6 {
		t.Fatal("missing embedded cases")
	}
	capture := revieweval.Capture{SchemaVersion: "1.0", SuiteSHA256: revieweval.SuiteSHA256(), Evidence: "controlled", StartedAt: time.Now().UTC(), CaseTimeoutMS: 1000,
		Runtime: review.RuntimeIdentity{Integration: "exec", Model: "controlled", CodexVersion: "fixture", BinarySHA256: strings.Repeat("a", 64)}}
	for _, c := range suite.Cases {
		capture.Results = append(capture.Results, revieweval.Result{CaseID: c.ID, BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), PatchSHA256: strings.Repeat("b", 64), State: "succeeded", DurationMS: 10,
			Report: &review.FindingsReport{SchemaVersion: "1.0", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), Findings: []review.Finding{}}})
	}
	capture.Results[0].Report.Findings = []review.Finding{{Title: "zero count", Description: "Divides by zero.", Severity: "HIGH", Confidence: .9, Path: "divider.py", StartLine: 2, EndLine: 2}}
	capturePath := filepath.Join(directory, "capture.json")
	writeTestJSON(t, capturePath, capture)
	pendingPath := filepath.Join(directory, "pending.json")
	run(true, "-capture", capturePath, "-out", pendingPath)
	var pending revieweval.Report
	readTestJSON(t, pendingPath, &pending)
	if pending.Summary.PendingFindings != 1 || pending.Summary.Recall != nil || pending.CaptureSHA256 == "" {
		t.Fatal("unjudged findings claimed quality")
	}
	if pending.SeverityCalibration == nil || pending.SeverityCalibration.PendingIssues != 1 || pending.SeverityCalibration.ExactMatchRate != nil {
		t.Fatal("unjudged CLI output claimed severity accuracy")
	}
	labels := revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: pending.CaptureSHA256, Reviewer: "controlled-test", Decisions: []revieweval.Decision{{CaseID: "zero-division", FindingIndex: 0, IssueID: "zero-count", Reason: "Matches the lost guard."}}}
	labelsPath, scoredPath := filepath.Join(directory, "labels.json"), filepath.Join(directory, "scored.json")
	writeTestJSON(t, labelsPath, labels)
	run(true, "-capture", capturePath, "-adjudications", labelsPath, "-out", scoredPath)
	var scored revieweval.Report
	readTestJSON(t, scoredPath, &scored)
	if scored.Summary.TruePositives != 1 || scored.Summary.FalseNegatives != 3 || scored.Evidence != "controlled" {
		t.Fatal("incorrect scored evidence")
	}
	calibration := scored.SeverityCalibration
	if calibration == nil || calibration.Policy != "first-adjudicated-match-v1" || calibration.ExactMatchRate == nil || calibration.ExactMatchRate.Denominator != 1 || calibration.ExactMatches != 1 || calibration.MissedIssues != 3 || calibration.ConfusionMatrix["HIGH"]["HIGH"] != 1 {
		t.Fatal("CLI did not serialize severity calibration")
	}
	if calibration.Issues[0].FindingIndex == nil || *calibration.Issues[0].FindingIndex != 0 || calibration.Issues[1].FindingIndex != nil || calibration.Issues[1].ReportedSeverity != "" {
		t.Fatal("CLI lost zero finding index or fabricated a missed prediction")
	}
	info, err := os.Stat(scoredPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("report is not private")
	}
	before, _ := os.ReadFile(scoredPath)
	run(false, "-mode", "suite", "-out", scoredPath)
	after, _ := os.ReadFile(scoredPath)
	if string(before) != string(after) {
		t.Fatal("overwrote prior evidence")
	}
	labels.CaptureSHA256 = strings.Repeat("0", 64)
	writeTestJSON(t, labelsPath, labels)
	rejected := filepath.Join(directory, "rejected.json")
	run(false, "-capture", capturePath, "-adjudications", labelsPath, "-out", rejected)
	if _, err := os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatal("invalid input left a report")
	}
	missingPrice := filepath.Join(directory, "missing-price.json")
	writeTestJSON(t, missingPrice, map[string]any{"schemaVersion": "1.0", "model": "controlled", "inputPerMillion": 1, "outputPerMillion": 2})
	run(false, "-capture", capturePath, "-pricing", missingPrice, "-out", filepath.Join(directory, "bad-pricing-report.json"))
	if len(scored.ScorerSHA256) != 64 {
		t.Fatal("scoring binary identity missing")
	}
	for _, selection := range []struct {
		name   string
		cases  int
		misses int
	}{{"starter-v1", 6, 4}, {"extended-v1", 12, 9}} {
		t.Run("capture_uses_deployment_runner/"+selection.name, func(t *testing.T) {
			directory := t.TempDir()
			docker := dockerFixture(t, directory)
			credential := filepath.Join(directory, "dummy-credential.json")
			writeTestJSON(t, credential, map[string]string{"servicePrincipal": "evaluation-test", "token": "dummy-evaluation-key"})
			capturedPath := filepath.Join(directory, "deployment-capture.json")
			run(true, "-mode", "capture", "-suite", selection.name, "-out", capturedPath, "-docker", docker, "-docker-host", "unix:///tmp/evaluation-fixture.sock", "-image", "sha256:"+strings.Repeat("a", 64), "-model", "controlled-model", "-gateway-base-url", "https://model.example.invalid", "-service-principal", "evaluation-test", "-credential-file", credential, "-timeout", "5s")
			var captured revieweval.Capture
			readTestJSON(t, capturedPath, &captured)
			if captured.Runtime.ModelTransport != "stdio" || captured.Runtime.ImageID == "" || len(captured.Results) != selection.cases || len(captured.EvaluatorSHA256) != 64 {
				t.Fatal("missing Docker deployment identity")
			}
			for _, result := range captured.Results {
				if result.State != "succeeded" || result.Report == nil || result.Usage == nil || !result.Usage.Complete || result.Usage.Requests != 0 {
					t.Fatal("CLI capture failed", result)
				}
			}
			paths, err := os.ReadFile(filepath.Join(directory, "worktrees"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range strings.Fields(string(paths)) {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("temporary evaluation workspace remains")
				}
			}
			data, _ := os.ReadFile(capturedPath)
			if strings.Contains(string(data), "dummy-evaluation-key") {
				t.Fatal("credential escaped into capture")
			}
			scoredPath := filepath.Join(directory, "scored.json")
			run(true, "-capture", capturedPath, "-out", scoredPath)
			var result revieweval.Report
			readTestJSON(t, scoredPath, &result)
			if result.Summary.Cases != selection.cases || result.Summary.FalseNegatives != selection.misses {
				t.Fatal("score did not resolve the captured suite")
			}
		})
	}
}

// External Docker/Worker transport fixture; no network or real model calls.
func dockerFixture(t *testing.T, directory string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(directory)
	script := "#!" + python + "\nimport json,os,sys\ndirectory=" + string(quoted) + "\n" + `
args=sys.argv[1:]
if args[0]=="image":
    print(json.dumps([dict(Id="sha256:"+"a"*64,Os="linux",Architecture="arm64")]))
elif args[0]=="create":
    assert "--model-relay" in args and "--network=none" in args
    with open(directory+"/"+args[args.index("--name")+1],"w") as f: json.dump(args,f)
    print("b"*64)
elif args[0]=="start":
    with open(directory+"/"+args[-1]) as f: created=json.load(f)
    if "--probe" in created:
        print(json.dumps(dict(modelRelaySupported=True,gatewaySupported=True,sandboxVerified=True,profile=dict(version="fixture",binaryPath="/opt/codex/codex",binarySha256="c"*64,integration="exec",sandbox="read-only",ephemeral=True,requiredFlags=["--sandbox","--ephemeral","--output-schema","--output-last-message","--json","--ignore-user-config","--ignore-rules","--no-daemon","--ask-for-approval","--model"]))))
    else:
        initial=json.loads(sys.stdin.readline())
        assert "dummy-evaluation-key" not in json.dumps(initial)
        request=initial["input"]
        with open(directory+"/worktrees","a") as f: f.write(request["worktreePath"]+"\n")
        assert "diff --git" in request["patch"]
        print(json.dumps(dict(type="result",result=dict(report=dict(schemaVersion="1.0",baseSha=request["baseSha"],headSha=request["headSha"],findings=[])))),flush=True)
elif args[0]=="rm":
    os.remove(directory+"/"+args[-1])
`
	path := filepath.Join(directory, "docker-fixture")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
