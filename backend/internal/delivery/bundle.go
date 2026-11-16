// Package delivery persists immutable plan packages independently of the live
// in-memory workbench. Saving a package grants no approval or execution rights.
package delivery

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/repository"
	"agent-platform/backend/internal/review"
)

var (
	ErrConfiguration = errors.New("invalid archive database configuration")
	ErrUnavailable   = errors.New("archive database unavailable")
	ErrNotFound      = errors.New("delivery package not found")
	ErrConflict      = errors.New("archive request does not match saved input")
	ErrInvalid       = errors.New("archive source or evidence is inconsistent")
	ErrCorrupt       = errors.New("saved delivery package failed integrity checks")
)

const MaxBundleBytes = 8 << 20

type Request struct {
	TenantID       string
	ChangeID       string
	PlanID         string
	PlanHash       string
	IdempotencyKey string
}

func (r Request) Valid() bool {
	return r.TenantID != "" && len(r.TenantID) <= 256 && r.ChangeID != "" && len(r.ChangeID) <= 256 && r.PlanID != "" && len(r.PlanID) <= 256 && len(r.PlanHash) == 64 && r.IdempotencyKey != "" && strings.TrimSpace(r.IdempotencyKey) == r.IdempotencyKey && len(r.IdempotencyKey) <= 256
}
func (r Request) digest() string {
	return planning.Hash(struct{ ChangeID, PlanID, PlanHash string }{r.ChangeID, r.PlanID, r.PlanHash})
}

type Evidence struct {
	ServiceID string           `json:"serviceId"`
	Execution review.Execution `json:"execution"`
	Content   []byte           `json:"content,omitempty"`
}
type Bundle struct {
	SchemaVersion string          `json:"schemaVersion"`
	ID            string          `json:"id"`
	TenantID      string          `json:"tenantId"`
	ChangeID      string          `json:"changeId"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	Environment   string          `json:"environment"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	Snapshot      change.Snapshot `json:"snapshot"`
	Plan          planning.Plan   `json:"plan"`
	Evidence      []Evidence      `json:"evidence"`
	Digest        string          `json:"digest"`
}

func (b Bundle) Public() Bundle {
	b = planning.Clone(b)
	for i := range b.Evidence {
		b.Evidence[i].Content = nil
	}
	return b
}
func (b Bundle) digest() string { b.Digest = ""; return planning.Hash(b) }
func (b Bundle) Valid() bool {
	if b.SchemaVersion != "delivery-archive.v1" || !strings.HasPrefix(b.ID, "delivery-") || b.TenantID == "" || b.ChangeID == "" || strings.TrimSpace(b.Title) == "" || len(b.Title) > 256 || len(b.Description) > 4096 || b.CreatedBy == "" || b.CreatedAt.IsZero() || b.Snapshot.State != "READY" || len(b.Snapshot.Services) == 0 || len(b.Snapshot.Services) > 20 || b.Plan.SchemaVersion != "change-plan.v1" || b.Plan.TenantID != b.TenantID || b.Plan.Environment != b.Environment || b.Plan.SnapshotID != b.Snapshot.ID || b.Plan.SnapshotHash != b.Snapshot.InputHash || (b.Plan.State != "READY_FOR_REVIEW" && b.Plan.State != "BLOCKED") || len(b.Plan.Services) != len(b.Snapshot.Services) || len(b.Evidence) > 20 {
		return false
	}
	plan := b.Plan
	plan.Hash = ""
	if planning.Hash(plan) != b.Plan.Hash {
		return false
	}
	services := map[string]change.ServiceSnapshot{}
	for _, s := range b.Snapshot.Services {
		if _, exists := services[s.ServiceID]; exists || s.ServiceID == "" || s.Repository == nil || s.Repository.Provider != "gitlab" || !repository.ValidObjectID(s.Repository.HeadSHA) || !repository.ValidObjectID(s.Repository.BaseSHA) || !repository.ValidObjectID(s.Repository.TargetSHA) {
			return false
		}
		services[s.ServiceID] = s
	}
	evidence := map[string]Evidence{}
	for _, e := range b.Evidence {
		if _, exists := evidence[e.ServiceID]; exists {
			return false
		}
		evidence[e.ServiceID] = e
	}
	seen := map[string]bool{}
	for _, s := range b.Plan.Services {
		frozen, ok := services[s.ServiceID]
		if !ok || seen[s.ServiceID] || s.HeadSHA != frozen.Repository.HeadSHA || s.BaseSHA != frozen.Repository.BaseSHA {
			return false
		}
		seen[s.ServiceID] = true
		e, exists := evidence[s.ServiceID]
		if s.Review.ExecutionID == "" {
			if exists || s.Review.State == "SUCCEEDED" {
				return false
			}
			continue
		}
		if !exists || e.Execution.ID != s.Review.ExecutionID || e.Execution.TaskID != s.Review.TaskID || e.Execution.TenantID != b.TenantID || e.Execution.BaseSHA != s.BaseSHA || e.Execution.HeadSHA != s.HeadSHA || e.Execution.WorkspaceID == "" || e.Execution.WorkspaceVersion == 0 {
			return false
		}
		execution := e.Execution
		if execution.State == review.ExecutionSucceeded {
			metadata := execution.Artifact
			if s.Review.State != "SUCCEEDED" || metadata == nil || metadata.ID != s.Review.ArtifactID || metadata.Type != artifact.TypePRReviewFindings || metadata.TenantID != b.TenantID || metadata.TaskID != execution.TaskID || metadata.WorkspaceID != execution.WorkspaceID || metadata.MediaType != "application/json" || metadata.SizeBytes != int64(len(e.Content)) || len(e.Content) > review.MaxFindingsBytes || fmt.Sprintf("%x", sha256.Sum256(e.Content)) != metadata.SHA256 {
				return false
			}
			if _, err := review.ParseFindings(e.Content, s.BaseSHA, s.HeadSHA); err != nil {
				return false
			}
		} else if (execution.State != review.ExecutionFailed && execution.State != review.ExecutionCanceled) || s.Review.State == "SUCCEEDED" || execution.Artifact != nil || len(e.Content) != 0 {
			return false
		}
	}
	for service := range evidence {
		if !seen[service] {
			return false
		}
	}
	for _, a := range b.Plan.Actions {
		if a.Target.TenantID != b.TenantID || a.Target.Environment != b.Environment {
			return false
		}
		for _, p := range a.Proposals {
			if !seen[p.ServiceID] {
				return false
			}
		}
	}
	return true
}
func decode(data []byte, digest string) (Bundle, error) {
	var b Bundle
	// JSONB emits formatting spaces on read; bound wire bytes separately from
	// the compact document accepted on write so valid large records stay readable.
	if len(data) > 2*MaxBundleBytes || json.Unmarshal(data, &b) != nil || !b.Valid() || b.Digest != digest || b.digest() != digest {
		return Bundle{}, ErrCorrupt
	}
	canonical, err := json.Marshal(b)
	if err != nil || len(canonical) > MaxBundleBytes {
		return Bundle{}, ErrCorrupt
	}
	return b, nil
}
