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
	"strings"
	"syscall"
	"time"

	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/httpapi"
)

func main() {
	// 让 HTTP 层的键值日志以 JSON 写入 stderr，便于按 requestId、taskId 检索。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	handler := httpapi.NewHandler()
	gitlabBaseURL := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_GITLAB_BASE_URL"))
	gitlabToken := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_GITLAB_TOKEN"))
	workspaceRoot := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_WORKSPACE_ROOT"))
	if gitlabBaseURL == "" && gitlabToken == "" && workspaceRoot == "" {
		log.Print("GitLab and Workspace preparation are not configured; Workspace operations will fail closed")
	} else {
		if gitlabBaseURL == "" || gitlabToken == "" || workspaceRoot == "" {
			log.Fatal("AGENT_PLATFORM_GITLAB_BASE_URL, AGENT_PLATFORM_GITLAB_TOKEN and AGENT_PLATFORM_WORKSPACE_ROOT must be configured together")
		}
		verifier, err := gitlab.NewVerifier(gitlabBaseURL, gitlabToken, &http.Client{Timeout: 5 * time.Second})
		if err != nil {
			log.Fatalf("configure GitLab repository verifier: %v", err)
		}
		gitPreparer, err := gitworkspace.NewPreparer("git")
		if err != nil {
			log.Fatalf("configure Git workspace preparer: %v", err)
		}
		diffReader, err := gitworkspace.NewDiffReader("git")
		if err != nil {
			log.Fatalf("configure Git diff reader: %v", err)
		}
		workspacePreparer, err := gitlab.NewWorkspacePreparer(verifier, gitPreparer)
		if err != nil {
			log.Fatalf("configure GitLab Workspace preparer: %v", err)
		}
		handler, err = httpapi.NewHandlerWithWorkspaceServices(verifier, workspacePreparer, diffReader, workspaceRoot)
		if err != nil {
			log.Fatalf("configure Workspace Manager: %v", err)
		}
	}

	server := newServer(handler)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatalf("listen for API requests: %v", err)
	}
	// 信号只用于通知主流程停止接新请求；不要把已取消的 signalContext
	// 直接传给 Shutdown，否则在途请求没有收尾时间。
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent-platform API listening on %s", server.Addr)
	if err := serveUntilShutdown(signalContext, server, listener); err != nil {
		log.Fatalf("serve API requests: %v", err)
	}
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":8080",
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
