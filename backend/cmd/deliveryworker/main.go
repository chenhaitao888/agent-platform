package main

import (
	"agent-platform/backend/internal/apiapp"
	"agent-platform/backend/internal/orchestration"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service, settings, err := apiapp.SimulationWorkerFromEnvironment(ctx)
	if err != nil {
		log.Fatal("configure simulation worker failed")
	}
	defer service.Store.Close()
	if err := orchestration.Run(ctx, service, settings.TemporalAddress, settings.Namespace, settings.TaskQueue, func() { log.Print("delivery simulation worker ready") }); err != nil {
		log.Fatal("simulation worker stopped: ", err)
	}
}
