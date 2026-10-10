package review

import (
	"context"
	"errors"
	"unicode/utf8"
)

var (
	ErrInvalidRunInput   = errors.New("invalid review execution input")
	ErrExecutionFailed   = errors.New("review execution failed")
	ErrOutputUnavailable = errors.New("review output unavailable")
	ErrOutputTooLarge    = errors.New("review output exceeds limit")
)

type RunInput struct {
	WorktreePath string `json:"worktreePath"`
	BaseSHA      string `json:"baseSha"`
	HeadSHA      string `json:"headSha"`
	Patch        string `json:"patch"`
}

const MaxReviewPatchBytes = 1 << 20

func (input RunInput) Validate() error {
	if input.WorktreePath == "" || !validFindingsCommit(input.BaseSHA) || !validFindingsCommit(input.HeadSHA) || len(input.Patch) > MaxReviewPatchBytes || !utf8.ValidString(input.Patch) {
		return ErrInvalidRunInput
	}
	return nil
}

// Runner receives coordinates and a patch selected by the platform. Runtime
// binary, model, credentials and permission options are deployment concerns.
type Runner interface {
	Run(context.Context, RunInput) (FindingsReport, error)
}

type RuntimeIdentity struct {
	Integration      string `json:"integration"`
	ImageID          string `json:"imageId,omitempty"`
	CodexVersion     string `json:"codexVersion"`
	BinarySHA256     string `json:"binarySha256"`
	Model            string `json:"model"`
	SeccompPolicy    string `json:"seccompPolicy,omitempty"`
	SeccompSHA256    string `json:"seccompSha256,omitempty"`
	GatewayURL       string `json:"gatewayUrl,omitempty"`
	ServicePrincipal string `json:"servicePrincipal,omitempty"`
}

// RuntimeProfiler exposes verified deployment identity without credentials.
type RuntimeProfiler interface {
	RuntimeIdentity() RuntimeIdentity
}
