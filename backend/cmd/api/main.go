package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-platform/backend/internal/apiapp"
)

func main() {
	// 让 HTTP 层的键值日志以 JSON 写入 stderr，便于按 requestId、taskId 检索。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := apiapp.FromEnvironment(signalContext)
	if err != nil {
		if signalContext.Err() != nil {
			return
		}
		log.Fatalf("configure API deployment: %v", err)
	}

	server := newServer(application.Handler)
	server.Addr = application.ListenAddress
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatalf("listen for API requests: %v", err)
	}
	// 信号只用于通知主流程停止接新请求；不要把已取消的 signalContext
	// 直接传给 Shutdown，否则在途请求没有收尾时间。
	if !application.AuthenticationConfigured {
		log.Print("API authentication is not configured; business APIs return 401. Use authinit to generate credentials, set AGENT_PLATFORM_AUTH_FILE, then restart the API.")
	}
	log.Printf("agent-platform API listening on %s; Review enabled=%t; Authentication configured=%t", listener.Addr(), application.ReviewEnabled, application.AuthenticationConfigured)
	if err := serveUntilShutdownWithGrace(signalContext, server, listener, application.ShutdownGrace); err != nil {
		log.Fatalf("serve API requests: %v", err)
	}
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Prepare 仍是同步 clone；先给它一个有限但较长的响应窗口。
		// 将来改成异步 Activity 后，可以把全局 WriteTimeout 收紧。
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
}

func serveUntilShutdown(ctx context.Context, server *http.Server, listener net.Listener) error {
	return serveUntilShutdownWithGrace(ctx, server, listener, time.Minute)
}

func serveUntilShutdownWithGrace(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Shutdown 先关闭监听，再等待已有请求完成。它返回之前 main 不能退出，
		// 否则 Git Preparer 的 defer 清理仍可能被进程退出打断。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("gracefully shut down API server: %w", err)
		}
		if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
