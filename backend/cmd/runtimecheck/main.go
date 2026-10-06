package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"agent-platform/backend/internal/codex"
)

func main() {
	binary := flag.String("codex", "codex", "Codex CLI binary to inspect without starting a review")
	digest := flag.String("sha256", "", "optional expected binary SHA-256; always records the actual digest")
	flag.Parse()
	profile, err := codex.Probe(context.Background(), *binary, *digest)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(profile); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
