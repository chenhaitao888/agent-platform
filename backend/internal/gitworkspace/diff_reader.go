package gitworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"agent-platform/backend/internal/repository"
)

var (
	ErrGitDiffOperation = errors.New("Git diff operation failed")
)

const maxDiffBytes = 1 << 20

type DiffReader struct {
	gitBinary string
}

func NewDiffReader(gitBinary string) (*DiffReader, error) {
	resolved, err := exec.LookPath(strings.TrimSpace(gitBinary))
	if err != nil {
		return nil, fmt.Errorf("find Git executable: %w", err)
	}
	return &DiffReader{gitBinary: resolved}, nil
}

func (r *DiffReader) Read(ctx context.Context, input repository.DiffInput) ([]byte, error) {
	// Diff 只能读取平台生成的 worktree，并且 revision 必须是完整对象 ID。
	// 这样即使未来出现新的非 HTTP 调用方，也不能把任意 Git 参数塞进命令。
	if _, err := validateDestination(input.WorktreePath); err != nil {
		return nil, fmt.Errorf("%w: %w: %v", repository.ErrDiffUnavailable, ErrGitDiffOperation, err)
	}
	if !isGitObjectID(input.BaseSHA) || !isGitObjectID(input.HeadSHA) {
		return nil, fmt.Errorf("%w: %w: base and head must be immutable Git object IDs", repository.ErrDiffUnavailable, ErrGitDiffOperation)
	}

	// 与 Java 的 ProcessBuilder 一样，这里直接传参数数组，不拼 shell 命令字符串。
	// 末尾的 -- 明确结束选项，后续即使增加路径参数，也不会被 Git 当成开关。
	command := exec.CommandContext(
		ctx,
		r.gitBinary,
		"-C", input.WorktreePath,
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--binary",
		input.BaseSHA,
		input.HeadSHA,
		"--",
	)
	command.Env = gitEnvironment("", Credentials{})
	output := limitedBuffer{limit: maxDiffBytes}
	var diagnostics cappedBuffer
	command.Stdout = &output
	command.Stderr = &diagnostics
	err := command.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %w: %v: %s", repository.ErrDiffUnavailable, ErrGitDiffOperation, err, strings.TrimSpace(diagnostics.String()))
	}
	if output.overflowed {
		return nil, repository.ErrDiffTooLarge
	}
	return output.buffer.Bytes(), nil
}

type limitedBuffer struct {
	buffer     bytes.Buffer
	limit      int
	overflowed bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining < len(value) {
		b.overflowed = true
	}
	if remaining > 0 {
		toWrite := value
		if len(toWrite) > remaining {
			toWrite = toWrite[:remaining]
		}
		_, _ = b.buffer.Write(toWrite)
	}
	// 和错误诊断缓冲区一样，即使达到上限也报告“本次全部接收”，让 Git 正常结束。
	return len(value), nil
}
