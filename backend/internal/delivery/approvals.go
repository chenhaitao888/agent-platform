package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"agent-platform/backend/internal/planning"
	"github.com/jackc/pgx/v5"
)

const approvalColumns = `id,tenant_id,bundle_id,bundle_digest,plan_hash,policy_hash,subject,reason,mode,CASE WHEN state IN ('APPROVED','CONSUMED') AND expires_at<=clock_timestamp() THEN 'EXPIRED' ELSE state END,version,created_at,expires_at,consumed_by,error_code,revoked_by,revoked_reason,revoked_at`

func readApproval(row pgx.Row) (Approval, error) {
	var a Approval
	err := row.Scan(&a.ID, &a.TenantID, &a.BundleID, &a.BundleDigest, &a.PlanHash, &a.PolicyHash, &a.Subject, &a.Reason, &a.Mode, &a.State, &a.Version, &a.CreatedAt, &a.ExpiresAt, &a.ConsumedBy, &a.ErrorCode, &a.RevokedBy, &a.RevokedReason, &a.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, ErrUnavailable
	}
	return a, nil
}
func (s *Store) GetApproval(ctx context.Context, tenant, bundle, id string) (Approval, error) {
	return readApproval(s.pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND bundle_id=$2 AND id=$3`, tenant, bundle, id))
}
func (s *Store) ListApprovals(ctx context.Context, tenant, bundle string) ([]Approval, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND bundle_id=$2 ORDER BY created_at DESC,id`, tenant, bundle)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := readApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
func (s *Store) Approve(ctx context.Context, in ApprovalInput) (Approval, bool, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || in.PolicyHash == "" || in.TTLSeconds < 1 || in.TTLSeconds > 86400 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 || !in.FindingsReviewed {
		return Approval{}, false, ErrInvalid
	}
	b, err := s.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return Approval{}, false, err
	}
	if b.Digest != in.ExpectedDigest {
		return Approval{}, false, ErrConflict
	}
	if !eligible(b) {
		return Approval{}, false, ErrUnready
	}
	digest := planning.Hash(in)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Approval{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"approve", in.TenantID, in.IdempotencyKey})); err != nil {
		return Approval{}, false, ErrUnavailable
	}
	var id, prior string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.approvals WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &prior)
	if err == nil {
		if prior != digest {
			return Approval{}, false, ErrConflict
		}
		a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND id=$2`, in.TenantID, id))
		return a, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, ErrUnavailable
	}
	id = "approval-" + rand.Text()
	a, err := readApproval(tx.QueryRow(ctx, `INSERT INTO agent_delivery.approvals(id,tenant_id,bundle_id,bundle_digest,plan_hash,policy_hash,subject,reason,mode,state,expires_at,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'SIMULATION','APPROVED',clock_timestamp()+make_interval(secs=>$9),$10,$11) RETURNING `+approvalColumns, id, in.TenantID, in.BundleID, b.Digest, b.Plan.Hash, in.PolicyHash, in.Subject, in.Reason, in.TTLSeconds, in.IdempotencyKey, digest))
	if err != nil {
		return Approval{}, false, err
	}
	if tx.Commit(ctx) != nil {
		return Approval{}, false, ErrUnavailable
	}
	return a, true, nil
}
func (s *Store) Revoke(ctx context.Context, in RevokeInput) (Approval, error) {
	if !validKey(in.IdempotencyKey) || in.Subject == "" || in.ExpectedVersion == 0 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 {
		return Approval{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Approval{}, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"revoke", in.TenantID, in.IdempotencyKey})); err != nil {
		return Approval{}, ErrUnavailable
	}
	digest := planning.Hash(in)
	var prior, id string
	err = tx.QueryRow(ctx, `SELECT request_hash,approval_id FROM agent_delivery.revocations WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&prior, &id)
	if err == nil {
		if prior != digest {
			return Approval{}, ErrConflict
		}
		return readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND bundle_id=$2 AND id=$3`, in.TenantID, in.BundleID, id))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrUnavailable
	}
	a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND bundle_id=$2 AND id=$3 FOR UPDATE`, in.TenantID, in.BundleID, in.ApprovalID))
	if err != nil {
		return a, err
	}
	if a.Version != in.ExpectedVersion {
		return Approval{}, ErrVersion
	}
	a, err = readApproval(tx.QueryRow(ctx, `UPDATE agent_delivery.approvals SET state='REVOKED',version=version+1,error_code='approval_revoked',revoked_by=$3,revoked_reason=$4,revoked_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 RETURNING `+approvalColumns, in.TenantID, in.ApprovalID, in.Subject, in.Reason))
	if err != nil {
		return Approval{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_delivery.revocations(tenant_id,idempotency_key,request_hash,approval_id,subject,reason) VALUES($1,$2,$3,$4,$5,$6)`, in.TenantID, in.IdempotencyKey, digest, in.ApprovalID, in.Subject, in.Reason); err != nil {
		return Approval{}, ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return Approval{}, ErrUnavailable
	}
	return a, nil
}
