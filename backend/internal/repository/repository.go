package repository

import (
	"context"
	"errors"

	"agent-platform/backend/internal/task"
)

var (
	ErrRepositoryNotFound      = errors.New("repository was not found or is not accessible")
	ErrBaseCommitNotFound      = errors.New("base commit was not found in repository")
	ErrHeadCommitNotFound      = errors.New("head commit was not found in repository")
	ErrVerificationUnavailable = errors.New("repository verification is unavailable")
	ErrDiffUnavailable         = errors.New("repository diff is unavailable")
	ErrDiffTooLarge            = errors.New("repository diff exceeds the supported size")
)

type ReferenceVerifier interface {
	Verify(context.Context, task.RepositoryReference) error
}

type UnavailableVerifier struct{}

func (UnavailableVerifier) Verify(context.Context, task.RepositoryReference) error {
	return ErrVerificationUnavailable
}

// DiffReader is the application-facing port for reading one immutable repository diff.
// The caller supplies coordinates obtained from a READY Workspace, not from an HTTP body.
type DiffInput struct {
	WorktreePath string
	BaseSHA      string
	HeadSHA      string
}

type DiffReader interface {
	Read(context.Context, DiffInput) ([]byte, error)
}

type UnavailableDiffReader struct{}

func (UnavailableDiffReader) Read(context.Context, DiffInput) ([]byte, error) {
	return nil, ErrDiffUnavailable
}
