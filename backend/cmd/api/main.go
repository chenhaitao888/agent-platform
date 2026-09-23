package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"agent-platform/backend/internal/connector/gitlab"
	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/httpapi"
)

func main() {
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

	server := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("agent-platform API listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
