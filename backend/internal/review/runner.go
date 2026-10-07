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
	WorktreePath string
	BaseSHA      string
	HeadSHA      string
	Patch        string
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
