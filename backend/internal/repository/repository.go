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
)

type ReferenceVerifier interface {
	Verify(context.Context, task.RepositoryReference) error
}

type UnavailableVerifier struct{}

func (UnavailableVerifier) Verify(context.Context, task.RepositoryReference) error {
	return ErrVerificationUnavailable
}
