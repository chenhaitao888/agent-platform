package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewServerBoundsConnectionLifetimes(t *testing.T) {
	server := newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("expected a five-second header timeout, got %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("expected finite read, write and idle timeouts, got read=%s write=%s idle=%s", server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestServeUntilShutdownWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen locally: %v", err)
	}
	defer listener.Close()
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	defer func() {
		select {
		case <-releaseRequest:
		default:
			close(releaseRequest)
		}
	}()
	server := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveUntilShutdown(ctx, server, listener) }()
	clientDone := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			clientDone <- err
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			clientDone <- fmt.Errorf("expected 204, got %d", response.StatusCode)
			return
		}
		clientDone <- nil
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel() // 与收到 SIGTERM 后的 NotifyContext 取消效果相同。
	select {
	case err := <-serveDone:
		t.Fatalf("server stopped before the active request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseRequest)
	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatalf("active request did not finish: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active request timed out")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("graceful shutdown failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after the active request finished")
	}
}
