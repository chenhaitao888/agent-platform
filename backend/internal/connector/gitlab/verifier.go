package gitlab

import (
	"context"
	"encoding/json"
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
const maxGitLabReferenceResponseBytes = 1 << 20

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

// Resolve 先把 master 冻结成 TargetSHA，再请 GitLab 求 TargetSHA 与 HeadSHA 的共同祖先。
// 类比 Java 先保存不可变的 CommitId；第二次请求不能再传会移动的分支名 master。
func (v *Verifier) Resolve(ctx context.Context, selection task.RepositoryReference) (task.RepositoryReference, error) {
	if !isFullGitObjectID(selection.HeadSHA) {
		return task.RepositoryReference{}, fmt.Errorf("%w: invalid head commit ID", repository.ErrVerificationUnavailable)
	}
	projectPath := projectAPIPath(selection.RepositoryID)
	branchPath := projectPath + "/repository/branches/" + url.PathEscape(task.ReviewTargetBranch)
	targetSHA, err := v.readCommitID(ctx, branchPath, repository.ErrTargetBranchNotFound, true)
	if err != nil {
		return task.RepositoryReference{}, err
	}
	// merge_base 的 404 还可能表示 head 不存在；先单独验证，HTTP 才能准确区分两种情况。
	if err := v.verifyGET(ctx, projectPath+"/repository/commits/"+url.PathEscape(selection.HeadSHA), repository.ErrHeadCommitNotFound); err != nil {
		return task.RepositoryReference{}, err
	}
	query := url.Values{}
	query.Add("refs[]", targetSHA)
	query.Add("refs[]", selection.HeadSHA)
	baseSHA, err := v.readCommitID(ctx, projectPath+"/repository/merge_base?"+query.Encode(), repository.ErrReviewBaseNotFound, false)
	if err != nil {
		return task.RepositoryReference{}, err
	}
	selection.TargetBranch = task.ReviewTargetBranch
	selection.TargetSHA = targetSHA
	selection.BaseSHA = baseSHA
	return selection, nil
}

func (v *Verifier) readCommitID(ctx context.Context, path string, notFoundError error, nested bool) (string, error) {
	// 分支响应使用 commit.id；merge_base 响应使用顶层 id。两者都只取完整 SHA，
	// 不把 GitLab 返回的标题、message 或其他正文带进领域对象。
	response, err := v.get(ctx, path, notFoundError)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGitLabReferenceResponseBytes+1))
	if err != nil || len(data) > maxGitLabReferenceResponseBytes {
		return "", fmt.Errorf("%w: read GitLab reference response", repository.ErrVerificationUnavailable)
	}
	var result struct {
		ID     string `json:"id"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("%w: decode GitLab reference response", repository.ErrVerificationUnavailable)
	}
	id := result.ID
	if nested {
		id = result.Commit.ID
	}
	if !isFullGitObjectID(id) {
		return "", fmt.Errorf("%w: GitLab returned an invalid commit ID", repository.ErrVerificationUnavailable)
	}
	return id, nil
}

func isFullGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
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
