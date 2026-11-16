package repository

import (
	"context"
	"strings"
	"unicode"

	"agent-platform/backend/internal/task"
)

// BranchSelection is constructed only after service/environment/repository authorization.
// It cannot carry a URL, credentials or caller-supplied commit IDs.
type BranchSelection struct{ RepositoryID, SourceBranch, TargetBranch string }
type BranchResolver interface {
	ResolveBranches(context.Context, BranchSelection) (task.RepositoryReference, error)
}

func ValidBranch(value string) bool {
	if value == "" || len(value) > 256 || value == "@" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// ValidObjectID accepts full SHA-1 and SHA-256 Git object IDs, never abbreviations.
func ValidObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		isDigit := r >= '0' && r <= '9'
		isLowerHex := r >= 'a' && r <= 'f'
		isUpperHex := r >= 'A' && r <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
}
