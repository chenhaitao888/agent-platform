package delivery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/strictjson"
	"github.com/jackc/pgx/v5"
)

// Ledger documents are updated under row locks, with a digest checked on every
// load. This also makes reading the entire operation prefix one atomic snapshot.
func readWriteDocument[T any](row pgx.Row) (T, error) {
	var result T
	var data []byte
	var digest string
	if err := row.Scan(&data, &digest); errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	} else if err != nil {
		return result, ErrUnavailable
	}
	if len(data) > 2*MaxBundleBytes || strictjson.Decode(data, &result) != nil || planning.Hash(result) != digest {
		return result, ErrCorrupt
	}
	return result, nil
}
func writeDocument(value any) ([]byte, string, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxBundleBytes {
		return nil, "", ErrInvalid
	}
	return data, planning.Hash(value), nil
}
func writeNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now) != nil {
		return now, ErrUnavailable
	}
	return now.UTC(), nil
}
func loadWriteApproval(ctx context.Context, tx pgx.Tx, tenant, id string) (WriteApproval, error) {
	return readWriteDocument[WriteApproval](tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id))
}
func saveWriteApproval(ctx context.Context, tx pgx.Tx, a WriteApproval) error {
	data, digest, err := writeDocument(a)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_delivery.write_approvals SET payload=$3,digest=$4 WHERE tenant_id=$1 AND id=$2`, a.TenantID, a.ID, data, digest); err != nil {
		return ErrUnavailable
	}
	return nil
}
func saveWriteExecution(ctx context.Context, tx pgx.Tx, r WriteExecution) error {
	data, digest, err := writeDocument(r)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_delivery.write_executions SET payload=$3,digest=$4 WHERE tenant_id=$1 AND id=$2`, r.TenantID, r.ID, data, digest); err != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) GetWriteApproval(ctx context.Context, tenant, bundle, id string) (WriteApproval, error) {
	return readWriteDocument[WriteApproval](s.pool.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_approvals WHERE tenant_id=$1 AND bundle_id=$2 AND id=$3`, tenant, bundle, id))
}
func (s *Store) GetWriteExecution(ctx context.Context, ref Ref) (WriteExecution, error) {
	return readWriteDocument[WriteExecution](s.pool.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_executions WHERE tenant_id=$1 AND id=$2`, ref.TenantID, ref.RunID))
}

func (s *Store) approveWrite(ctx context.Context, in ApprovalInput, bindings []WriteBinding) (WriteApproval, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteApproval{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"approve_write", in.TenantID, in.IdempotencyKey})); err != nil {
		return WriteApproval{}, false, ErrUnavailable
	}
	requestHash := planning.Hash(struct {
		Input    ApprovalInput
		Bindings []WriteBinding
	}{in, bindings})
	var id, prior string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.write_approvals WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &prior)
	if err == nil {
		if prior != requestHash {
			return WriteApproval{}, false, ErrConflict
		}
		a, err := loadWriteApproval(ctx, tx, in.TenantID, id)
		return a, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WriteApproval{}, false, ErrUnavailable
	}
	b, err := loadRow(tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.archives WHERE tenant_id=$1 AND id=$2`, in.TenantID, in.BundleID))
	if err != nil {
		return WriteApproval{}, false, err
	}
	now, err := writeNow(ctx, tx)
	if err != nil {
		return WriteApproval{}, false, err
	}
	a := WriteApproval{Approval: Approval{ID: "write-approval-" + rand.Text(), TenantID: in.TenantID, BundleID: b.ID, BundleDigest: b.Digest, PlanHash: b.Plan.Hash, PolicyHash: in.PolicyHash, Subject: in.Subject, Reason: in.Reason, Mode: "WRITE", State: "APPROVED", Version: 1, CreatedAt: now, ExpiresAt: now.Add(time.Duration(in.TTLSeconds) * time.Second)}, Bindings: bindings}
	data, digest, err := writeDocument(a)
	if err != nil {
		return WriteApproval{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.write_approvals(tenant_id,id,bundle_id,idempotency_key,request_hash,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.TenantID, a.ID, a.BundleID, in.IdempotencyKey, requestHash, data, digest); err != nil || tx.Commit(ctx) != nil {
		return WriteApproval{}, false, ErrUnavailable
	}
	return a, true, nil
}

func (s *Store) startWrite(ctx context.Context, in WriteStartInput, stamp string) (WriteExecution, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteExecution{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"start_write", in.TenantID, in.IdempotencyKey})); err != nil {
		return WriteExecution{}, false, ErrUnavailable
	}
	requestHash := planning.Hash(in)
	var prior, id string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.write_executions WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &prior)
	if err == nil {
		if prior != requestHash {
			return WriteExecution{}, false, ErrConflict
		}
		r, err := readWriteDocument[WriteExecution](tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_executions WHERE tenant_id=$1 AND id=$2`, in.TenantID, id))
		return r, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WriteExecution{}, false, ErrUnavailable
	}
	a, err := loadWriteApproval(ctx, tx, in.TenantID, in.ApprovalID)
	if err != nil {
		return WriteExecution{}, false, err
	}
	now, err := writeNow(ctx, tx)
	if err != nil {
		return WriteExecution{}, false, err
	}
	if a.BundleID != in.BundleID || a.BundleDigest != in.ExpectedDigest || !activeWriteApproval(a, "", now) {
		return WriteExecution{}, false, ErrApproval
	}
	r := WriteExecution{ID: "write-" + rand.Text(), TenantID: in.TenantID, BundleID: in.BundleID, ApprovalID: a.ID, Subject: in.Subject, PolicyHash: stamp, Mode: "WRITE", State: "QUEUED", CreatedAt: now, Operations: []WriteOperation{}}
	r.WorkflowID = "delivery-" + r.ID
	for _, binding := range a.Bindings {
		r.Operations = append(r.Operations, WriteOperation{ID: "write-operation-" + rand.Text(), ActionID: binding.ActionID, ActionHash: binding.ActionHash, State: "NOT_STARTED"})
	}
	a.State = "CONSUMED"
	a.ConsumedBy = r.ID
	a.Version++
	if err := saveWriteApproval(ctx, tx, a); err != nil {
		return WriteExecution{}, false, err
	}
	data, digest, err := writeDocument(r)
	if err != nil {
		return WriteExecution{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.write_executions(tenant_id,id,bundle_id,approval_id,workflow_id,idempotency_key,request_hash,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.TenantID, r.ID, r.BundleID, r.ApprovalID, r.WorkflowID, in.IdempotencyKey, requestHash, data, digest); err != nil {
		return WriteExecution{}, false, ErrUnavailable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.write_outbox(id,tenant_id,run_id,kind) VALUES($1,$2,$3,'write.requested.v1')`, "write-outbox-"+rand.Text(), r.TenantID, r.ID); err != nil || tx.Commit(ctx) != nil {
		return WriteExecution{}, false, ErrUnavailable
	}
	return r, true, nil
}

// All ledger transitions lock approval before execution. No network operation is
// performed under these locks. Revocation and dispatch therefore serialize.
func (s *Store) changeWrite(ctx context.Context, ref Ref, fn func(*WriteExecution, WriteApproval, time.Time) error) error {
	initial, err := s.GetWriteExecution(ctx, ref)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	a, err := loadWriteApproval(ctx, tx, ref.TenantID, initial.ApprovalID)
	if err != nil {
		return err
	}
	r, err := readWriteDocument[WriteExecution](tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_executions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID))
	if err != nil {
		return err
	}
	if r.ApprovalID != a.ID || r.BundleID != a.BundleID || r.Mode != "WRITE" || a.Mode != "WRITE" {
		return ErrCorrupt
	}
	now, err := writeNow(ctx, tx)
	if err != nil {
		return err
	}
	if err := fn(&r, a, now); err != nil {
		return err
	}
	if err := saveWriteExecution(ctx, tx, r); err != nil {
		return err
	}
	if tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) revokeWrite(ctx context.Context, in RevokeInput) (WriteApproval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteApproval{}, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"revoke_write", in.TenantID, in.IdempotencyKey})); err != nil {
		return WriteApproval{}, ErrUnavailable
	}
	var prior, id string
	err = tx.QueryRow(ctx, `SELECT request_hash,approval_id FROM agent_delivery.write_revocations WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&prior, &id)
	if err == nil {
		if prior != planning.Hash(in) {
			return WriteApproval{}, ErrConflict
		}
		return loadWriteApproval(ctx, tx, in.TenantID, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WriteApproval{}, ErrUnavailable
	}
	a, err := loadWriteApproval(ctx, tx, in.TenantID, in.ApprovalID)
	if err != nil {
		return a, err
	}
	if a.BundleID != in.BundleID {
		return WriteApproval{}, ErrNotFound
	}
	if a.Version != in.ExpectedVersion {
		return WriteApproval{}, ErrVersion
	}
	// Keep the original revocation identity/reason immutable. Exact-key replay
	// is handled above; a second key must not rewrite that audit fact.
	if a.State == "REVOKED" {
		return WriteApproval{}, ErrApproval
	}
	now, err := writeNow(ctx, tx)
	if err != nil {
		return WriteApproval{}, err
	}
	a.State = "REVOKED"
	a.Version++
	a.RevokedBy = in.Subject
	a.RevokedReason = in.Reason
	a.RevokedAt = &now
	a.ErrorCode = "approval_revoked"
	if err := saveWriteApproval(ctx, tx, a); err != nil {
		return WriteApproval{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.write_revocations(tenant_id,idempotency_key,request_hash,approval_id) VALUES($1,$2,$3,$4)`, in.TenantID, in.IdempotencyKey, planning.Hash(in), a.ID); err != nil || tx.Commit(ctx) != nil {
		return WriteApproval{}, ErrUnavailable
	}
	return a, nil
}
