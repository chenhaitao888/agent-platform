package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"agent-platform/backend/internal/codex"
)

func main() {
	config := codex.DockerConfig{UID: os.Getuid(), GID: os.Getgid()}
	flag.StringVar(&config.Binary, "docker", "docker", "Docker client binary")
	flag.StringVar(&config.Endpoint, "docker-host", "", "local Docker Unix socket")
	flag.StringVar(&config.Image, "image", "", "immutable local Worker image")
	flag.StringVar(&config.WorkspaceRoot, "workspace-root", "", "existing service workspace root")
	flag.StringVar(&config.Model, "model", "", "deployment model name (no inference during probe)")
	flag.DurationVar(&config.Timeout, "timeout", time.Minute, "review execution limit")
	flag.IntVar(&config.UID, "uid", config.UID, "non-root workspace owner UID")
	flag.IntVar(&config.GID, "gid", config.GID, "workspace owner GID")
	flag.Parse()
	runner, err := codex.NewDockerRunner(context.Background(), config)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(runner.Profile()); err != nil {
		os.Exit(1)
	}
}
