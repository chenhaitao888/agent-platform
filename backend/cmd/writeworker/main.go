package main

import (
	"context"
	"crypto/rand"
	"log"
	"os"
	"os/signal"
	"syscall"

	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/orchestration"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service, settings, err := apiapp.WriteWorkerFromEnvironment(ctx)
	if err != nil {
		log.Fatal("configure write worker failed")
	}
	defer service.Store.Close()
	if err := orchestration.RunWrites(ctx, service, settings.TemporalAddress, settings.Namespace, settings.TaskQueue, "writer-"+rand.Text(), func() { log.Print("delivery write worker ready") }); err != nil {
		log.Fatal("write worker stopped: ", err)
	}
}
