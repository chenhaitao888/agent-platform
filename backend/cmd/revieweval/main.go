// revieweval captures synthetic reviews using the deployment Docker boundary,
// or scores an existing capture entirely offline with explicit human labels.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-platform/backend/internal/codex"
	"agent-platform/backend/internal/revieweval"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("revieweval", flag.ContinueOnError)
	mode := flags.String("mode", "score", "suite, capture (model calls), or score (offline)")
	out := flags.String("out", "", "new JSON output file (0600, never overwritten)")
	capturePath := flags.String("capture", "", "capture file to score")
	labelsPath := flags.String("adjudications", "", "optional human labels bound to the capture SHA-256")
	pricingPath := flags.String("pricing", "", "optional explicit USD rate snapshot (offline scoring only)")
	git := flags.String("git", "git", "Git executable for synthetic snapshots")
	config := codex.DockerConfig{UID: os.Getuid(), GID: os.Getgid(), ModelRelay: true}
	flags.StringVar(&config.Binary, "docker", "docker", "Docker client binary")
	flags.StringVar(&config.Endpoint, "docker-host", "", "local Docker Unix socket")
	flags.StringVar(&config.Image, "image", "", "immutable local Worker image")
	flags.StringVar(&config.Model, "model", "", "deployment model")
	flags.StringVar(&config.SeccompPolicy, "seccomp-policy", codex.SeccompDockerDefault, "reviewed Worker seccomp policy")
	flags.DurationVar(&config.Timeout, "timeout", 90*time.Second, "per-case budget including Git preparation (1s to 2m)")
	baseURL := flags.String("gateway-base-url", "", "deployment HTTPS Responses endpoint")
	principal := flags.String("service-principal", "", "deployment service identity")
	credential := flags.String("credential-file", "", "private credential file path outside the temporary workspace")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *out == "" || (*mode != "suite" && *mode != "score" && *mode != "capture") {
		return errors.New("choose -mode suite|score|capture and a new -out file")
	}
	// Reserve the output before any inference; a typo cannot discard paid results
	// or silently replace an earlier capture or adjudications file.
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("cannot create evaluation output (file must not exist)")
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(*out)
		}
	}()
	var output any
	switch *mode {
	case "suite":
		output = revieweval.DefaultSuite()
	case "score":
		var capture revieweval.Capture
		if err := readJSON(*capturePath, &capture); err != nil {
			return err
		}
		var labels *revieweval.Adjudications
		if *labelsPath != "" {
			labels = &revieweval.Adjudications{}
			if err := readJSON(*labelsPath, labels); err != nil {
				return err
			}
		}
		var pricing *revieweval.Pricing
		if *pricingPath != "" {
			pricing = &revieweval.Pricing{}
			if err := readJSON(*pricingPath, pricing); err != nil {
				return err
			}
		}
		var report revieweval.Report
		report, err = revieweval.ScoreWithPricing(capture, labels, pricing)
		if err == nil {
			report.ScorerSHA256, err = evaluatorSHA256()
		}
		output = report
	case "capture":
		if config.Timeout < time.Second || config.Timeout > 2*time.Minute {
			return errors.New("evaluation case timeout must be 1s to 2m")
		}
		root, createErr := os.MkdirTemp("", "agent-platform-evaluation-")
		if createErr != nil {
			return errors.New("cannot create private evaluation workspace")
		}
		defer func() { _ = os.RemoveAll(root) }()
		config.WorkspaceRoot = root
		config.Gateway = &codex.GatewayConfig{BaseURL: *baseURL, ServicePrincipal: *principal, CredentialFile: *credential}
		runner, createErr := codex.NewDockerRunner(ctx, config)
		if createErr != nil {
			return errors.New("evaluation Worker preflight failed")
		}
		var captured revieweval.Capture
		// Hash before inference so identity errors cannot discard a paid run.
		digest, hashErr := evaluatorSHA256()
		if hashErr != nil {
			return errors.New("cannot identify evaluation executable")
		}
		captured, err = revieweval.Record(ctx, runner, revieweval.Config{WorkspaceRoot: root, GitBinary: *git, Timeout: config.Timeout, Evidence: "deployment"})
		captured.EvaluatorSHA256 = digest
		output = captured
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return errors.New("cannot write evaluation output")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot finish evaluation output")
	}
	complete = true
	return nil
}

func evaluatorSHA256() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func readJSON(path string, target any) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return revieweval.ErrInvalidEvaluation
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	const limit = 8 << 20
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return revieweval.ErrInvalidEvaluation
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return revieweval.ErrInvalidEvaluation
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return revieweval.ErrInvalidEvaluation
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return revieweval.ErrInvalidEvaluation
	}
	return nil
}
