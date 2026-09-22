package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"agent-platform/backend/internal/gitworkspace"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/task"
)

type gitPreparer interface {
	Prepare(context.Context, gitworkspace.Input) error
}

type WorkspacePreparer struct {
	verifier *Verifier
	git      gitPreparer
}

func NewWorkspacePreparer(verifier *Verifier, git gitPreparer) (*WorkspacePreparer, error) {
	if verifier == nil {
		return nil, errors.New("GitLab verifier is required")
	}
	if git == nil {
		return nil, errors.New("Git preparer is required")
	}
	return &WorkspacePreparer{verifier: verifier, git: git}, nil
}

func (p *WorkspacePreparer) Prepare(ctx context.Context, reference task.RepositoryReference, destination string) error {
	// clone URL 必须重新从已认证的 GitLab API 读取，不能接受浏览器或 Task 直接提交的 URL。
	// 这样 Workspace Manager 只持有稳定的 repositoryID，不会持有可能夹带凭据的地址。
	cloneURL, err := p.cloneURL(ctx, reference.RepositoryID)
	if err != nil {
		return err
	}
	return p.git.Prepare(ctx, gitworkspace.Input{
		CloneURL:    cloneURL,
		BaseSHA:     reference.BaseSHA,
		HeadSHA:     reference.HeadSHA,
		Destination: destination,
		Credentials: gitworkspace.Credentials{
			Username: "oauth2",
			Password: p.verifier.token,
		},
	})
}

func (p *WorkspacePreparer) cloneURL(ctx context.Context, repositoryID string) (string, error) {
	projectPath := projectAPIPath(repositoryID)
	response, err := p.verifier.get(ctx, projectPath, repository.ErrRepositoryNotFound)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	var project struct {
		HTTPURLToRepo string `json:"http_url_to_repo"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&project); err != nil {
		return "", fmt.Errorf("%w: decode GitLab project: %v", repository.ErrVerificationUnavailable, err)
	}
	cloneURL := strings.TrimSpace(project.HTTPURLToRepo)
	parsedCloneURL, err := url.Parse(cloneURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid GitLab clone URL", repository.ErrVerificationUnavailable)
	}
	parsedBaseURL, _ := url.Parse(p.verifier.baseURL)
	// GitLab API 的响应也是外部输入，不能因为它来自“受信服务”就直接交给带 token 的 Git。
	// 这里只允许同一 HTTPS host，且拒绝 userinfo/query/fragment，防止 token 被送往其他主机，
	// 也防止用户名或密码被偷偷塞进 clone URL。
	hasUnsupportedParts := parsedCloneURL.User != nil ||
		parsedCloneURL.RawQuery != "" ||
		parsedCloneURL.ForceQuery ||
		parsedCloneURL.Fragment != "" ||
		parsedCloneURL.Opaque != ""
	if parsedCloneURL.Scheme != "https" || parsedCloneURL.Host == "" || parsedCloneURL.Host != parsedBaseURL.Host || hasUnsupportedParts {
		return "", fmt.Errorf("%w: untrusted GitLab clone URL", repository.ErrVerificationUnavailable)
	}
	return parsedCloneURL.String(), nil
}
