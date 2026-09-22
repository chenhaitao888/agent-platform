package gitworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrGitOperation = errors.New("Git workspace operation failed")

type Credentials struct {
	Username string
	Password string
}

type Input struct {
	CloneURL    string
	BaseSHA     string
	HeadSHA     string
	Destination string
	Credentials Credentials
}

type Preparer struct {
	gitBinary string
}

func NewPreparer(gitBinary string) (*Preparer, error) {
	resolved, err := exec.LookPath(strings.TrimSpace(gitBinary))
	if err != nil {
		return nil, fmt.Errorf("find Git executable: %w", err)
	}
	return &Preparer{gitBinary: resolved}, nil
}

func (p *Preparer) Prepare(ctx context.Context, input Input) error {
	// 先验证由平台生成的目录边界和不可变 SHA。即使未来出现非 HTTP 调用方，
	// 也不能把任意路径或任意文本直接变成 Git 参数。
	workspaceDir, err := validateDestination(input.Destination)
	if err != nil {
		return err
	}
	if !isGitObjectID(input.BaseSHA) || !isGitObjectID(input.HeadSHA) {
		return fmt.Errorf("%w: base and head must be immutable Git object IDs", ErrGitOperation)
	}
	if err := os.Mkdir(workspaceDir, 0o700); err != nil {
		return fmt.Errorf("%w: create workspace directory: %v", ErrGitOperation, err)
	}
	// os.Mkdir 只会成功创建一个原本不存在的 workspace-{id} 目录。
	// 因此失败清理只删除本次亲手创建的目录，不会删除调用方已有目录。
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(workspaceDir)
		}
	}()

	// 这个脚本是运行时临时生成的，不是仓库中预先存在的文件。
	// 例如 Workspace 根目录是 /data/workspaces 时，它会短暂存在于：
	// /data/workspaces/workspace-1/git-askpass.sh。
	askPassPath := filepath.Join(workspaceDir, "git-askpass.sh")
	if err := os.WriteFile(askPassPath, []byte(askPassScript), 0o700); err != nil {
		return fmt.Errorf("%w: create credential helper: %v", ErrGitOperation, err)
	}
	environment := gitEnvironment(askPassPath, input.Credentials)
	repositoryDir := filepath.Join(workspaceDir, "repository.git")

	// clone URL 和目录是 argv，但 token 不是；Git 需要认证时会执行 GIT_ASKPASS 指向的脚本，
	// 脚本再从当前 Git 子进程的环境变量读取用户名和密码。
	if err := p.run(ctx, environment, "clone", "--bare", "--no-local", "--", input.CloneURL, repositoryDir); err != nil {
		return err
	}
	// 显式 fetch 两个不可变 SHA，避免只依赖默认分支碰巧包含目标 commit。
	if err := p.run(ctx, environment, "--git-dir", repositoryDir, "fetch", "--no-tags", "origin", input.BaseSHA, input.HeadSHA); err != nil {
		return err
	}
	// 从这里开始都是本地 Git 操作，不再需要远端凭据。立即删除 helper，并构造空凭据环境，
	// 缩短 token 能被子进程读取的时间范围。
	if err := os.Remove(askPassPath); err != nil {
		return fmt.Errorf("%w: remove credential helper: %v", ErrGitOperation, err)
	}
	localEnvironment := gitEnvironment("", Credentials{})
	if err := p.run(ctx, localEnvironment, "--git-dir", repositoryDir, "cat-file", "-e", input.BaseSHA+"^{commit}"); err != nil {
		return err
	}
	if err := p.run(ctx, localEnvironment, "--git-dir", repositoryDir, "cat-file", "-e", input.HeadSHA+"^{commit}"); err != nil {
		return err
	}
	if err := p.run(ctx, localEnvironment, "--git-dir", repositoryDir, "worktree", "add", "--detach", input.Destination, input.HeadSHA); err != nil {
		return err
	}

	completed = true
	return nil
}

func validateDestination(destination string) (string, error) {
	if !filepath.IsAbs(destination) || filepath.Base(destination) != "worktree" {
		return "", fmt.Errorf("%w: destination must be an absolute worktree path", ErrGitOperation)
	}
	workspaceDir := filepath.Dir(filepath.Clean(destination))
	if !strings.HasPrefix(filepath.Base(workspaceDir), "workspace-") || filepath.Dir(workspaceDir) == string(filepath.Separator) {
		return "", fmt.Errorf("%w: destination must belong to a generated workspace directory", ErrGitOperation)
	}
	return workspaceDir, nil
}

func (p *Preparer) run(ctx context.Context, environment []string, args ...string) error {
	// CommandContext 直接执行已经解析出的 Git 二进制，不经过 shell；args 也不会被 shell 再解释。
	command := exec.CommandContext(ctx, p.gitBinary, args...)
	// Cmd.Env 非 nil 时，Go 不会自动继承父进程的完整环境；这里传入的是 gitEnvironment 的白名单结果。
	command.Env = environment
	var output cappedBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: git %s: %v: %s", ErrGitOperation, args[0], err, strings.TrimSpace(output.String()))
	}
	return nil
}

func gitEnvironment(askPassPath string, credentials Credentials) []string {
	// 只保留 Git 正常联网可能需要的基础变量。特别是不复制 AGENT_PLATFORM_GITLAB_TOKEN，
	// 也不复制控制平面进程中可能存在的数据库、云平台等其他 secret。
	allowedKeys := map[string]struct{}{
		"GIT_EXEC_PATH": {},
		"HTTP_PROXY":    {},
		"HTTPS_PROXY":   {},
		"LANG":          {},
		"LC_ALL":        {},
		"NO_PROXY":      {},
		"PATH":          {},
		"SSL_CERT_DIR":  {},
		"SSL_CERT_FILE": {},
		"TMPDIR":        {},
		"http_proxy":    {},
		"https_proxy":   {},
		"no_proxy":      {},
	}
	environment := make([]string, 0, len(os.Environ())+7)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, allowed := allowedKeys[key]; allowed {
			environment = append(environment, item)
		}
	}
	return append(environment,
		// 专用变量只存在于受控 Git/askpass 子进程；它们既不进入 URL，也不进入 argv。
		"AGENT_PLATFORM_GIT_PASSWORD="+credentials.Password,
		"AGENT_PLATFORM_GIT_USERNAME="+credentials.Username,
		"GIT_ASKPASS="+askPassPath,
		"GIT_ASKPASS_REQUIRE=force",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		isUpperHex := character >= 'A' && character <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
}

type cappedBuffer struct {
	buffer bytes.Buffer
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	// 远端或 Git 错误可能输出很多内容。最多保留 64 KiB 供内部诊断，同时仍向命令报告
	// “全部写入”，避免因为诊断缓冲区已满而反过来改变 Git 进程行为。
	const limit = 64 << 10
	remaining := limit - b.buffer.Len()
	if remaining > 0 {
		toWrite := value
		if len(toWrite) > remaining {
			toWrite = toWrite[:remaining]
		}
		_, _ = b.buffer.Write(toWrite)
	}
	return len(value), nil
}

func (b *cappedBuffer) String() string {
	return b.buffer.String()
}

// Git 会用“Username ...”或“Password ...”作为第一个参数调用这个脚本，并从 stdout 读取答案。
// 脚本本身不含 token；真正的值只来自当前子进程环境，所以脚本文件泄露也不会直接泄露凭据。
const askPassScript = `#!/bin/sh
case "$1" in
  *Username*) printf '%s\n' "$AGENT_PLATFORM_GIT_USERNAME" ;;
  *) printf '%s\n' "$AGENT_PLATFORM_GIT_PASSWORD" ;;
esac
`
