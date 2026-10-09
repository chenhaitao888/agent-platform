package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-platform/backend/internal/codex"
)

func main() {
	root := flag.String("root", "/workspaces", "deployment workspace root")
	model := flag.String("model", "", "deployment model")
	timeout := flag.Duration("timeout", time.Minute, "execution timeout")
	digest := flag.String("sha256", "", "expected Codex binary SHA-256")
	probe := flag.Bool("probe", false, "inspect capabilities without inference")
	flag.Parse()
	if flag.NArg() != 0 {
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := codex.ServeWorker(ctx, codex.ExecConfig{Binary: "/opt/codex/codex", ExpectedBinarySHA256: *digest, WorkspaceRoot: *root, Model: *model, Timeout: *timeout}, *probe, os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
