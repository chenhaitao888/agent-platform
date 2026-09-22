package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

type Verifier struct {
	baseURL string
	token   string
	client  *http.Client
}

// gitLabProjectsAPIPath 是 GitLab REST API v4 定义的协议路径，不是某个项目的配置。
// 可变部分只有后面的项目 ID；例如 platform/agent-project 会被编码成 platform%2Fagent-project。
const gitLabProjectsAPIPath = "/api/v4/projects/"

func projectAPIPath(repositoryID string) string {
	return gitLabProjectsAPIPath + url.PathEscape(repositoryID)
}

func NewVerifier(baseURL, token string, client *http.Client) (*Verifier, error) {
	parsedBaseURL, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, errors.New("GitLab base URL must be a valid HTTPS URL")
	}
	hasUnsupportedParts := parsedBaseURL.User != nil ||
		parsedBaseURL.RawQuery != "" ||
		parsedBaseURL.ForceQuery ||
		parsedBaseURL.Fragment != "" ||
		parsedBaseURL.Opaque != ""
	if parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" || hasUnsupportedParts {
		return nil, errors.New("GitLab base URL must be a valid HTTPS URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("GitLab token is required")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	clientWithoutRedirects := *client
	// PRIVATE-TOKEN 只应发给配置的 GitLab。禁止自动跟随重定向，避免下游用 302
	// 把带凭据的请求引到另一台主机。
	clientWithoutRedirects.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Verifier{
		baseURL: strings.TrimRight(parsedBaseURL.String(), "/"),
		token:   strings.TrimSpace(token),
		client:  &clientWithoutRedirects,
	}, nil
}

func (v *Verifier) Verify(ctx context.Context, reference task.RepositoryReference) error {
	projectPath := projectAPIPath(reference.RepositoryID)
	if err := v.verifyGET(ctx, projectPath, repository.ErrRepositoryNotFound); err != nil {
		return err
	}
	if err := v.verifyGET(ctx, projectPath+"/repository/commits/"+url.PathEscape(reference.BaseSHA), repository.ErrBaseCommitNotFound); err != nil {
		return err
	}
	return v.verifyGET(ctx, projectPath+"/repository/commits/"+url.PathEscape(reference.HeadSHA), repository.ErrHeadCommitNotFound)
}

func (v *Verifier) verifyGET(ctx context.Context, path string, notFoundError error) error {
	response, err := v.get(ctx, path, notFoundError)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return nil
}

func (v *Verifier) get(ctx context.Context, path string, notFoundError error) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build GitLab request: %v", repository.ErrVerificationUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	// API token 放在 HTTP header，不拼进 URL。这样 URL、访问日志和错误文本都不会天然携带 token。
	request.Header.Set("PRIVATE-TOKEN", v.token)

	response, err := v.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: GitLab request failed: %v", repository.ErrVerificationUnavailable, err)
	}

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNotFound {
		return nil, notFoundError
	}
	return nil, fmt.Errorf("%w: GitLab returned HTTP %d", repository.ErrVerificationUnavailable, response.StatusCode)
}
