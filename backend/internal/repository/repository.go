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
	ErrTargetBranchNotFound    = errors.New("target branch was not found in repository")
	ErrReviewBaseNotFound      = errors.New("review base could not be computed")
	ErrVerificationUnavailable = errors.New("repository verification is unavailable")
	ErrDiffUnavailable         = errors.New("repository diff is unavailable")
	ErrDiffTooLarge            = errors.New("repository diff exceeds the supported size")
)

type ReferenceVerifier interface {
	Verify(context.Context, task.RepositoryReference) error
}

// ReferenceResolver turns a caller's repository/head selection into frozen target/base/head commits.
type ReferenceResolver interface {
	Resolve(context.Context, task.RepositoryReference) (task.RepositoryReference, error)
}

type ReferenceServices interface {
	ReferenceVerifier
	ReferenceResolver
}

type UnavailableVerifier struct{}

func (UnavailableVerifier) Verify(context.Context, task.RepositoryReference) error {
	return ErrVerificationUnavailable
}

func (UnavailableVerifier) Resolve(context.Context, task.RepositoryReference) (task.RepositoryReference, error) {
	return task.RepositoryReference{}, ErrVerificationUnavailable
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
