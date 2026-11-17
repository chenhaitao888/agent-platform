package delivery

import (
	"context"
	"encoding/json"
	"errors"

	"agent-platform/backend/internal/planning"
	"github.com/jackc/pgx/v5"
)

func compensationDigest(p CompensationPlan) string { p.Digest = ""; return planning.Hash(p) }
func readCompensation(row pgx.Row) (CompensationPlan, error) {
	p, err := readWriteDocument[CompensationPlan](row)
	if err != nil {
		return p, err
	}
	if p.SchemaVersion != "compensation-plan.v1" || p.Digest != compensationDigest(p) {
		return CompensationPlan{}, ErrCorrupt
	}
	return p, nil
}
func (s *Store) GetCompensation(ctx context.Context, tenant, bundle, id string) (CompensationPlan, error) {
	return readCompensation(s.pool.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.compensation_plans WHERE tenant_id=$1 AND source_bundle_id=$2 AND id=$3`, tenant, bundle, id))
}
func (s *Store) ListCompensations(ctx context.Context, tenant, bundle string) ([]CompensationPlan, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload,digest FROM agent_delivery.compensation_plans WHERE tenant_id=$1 AND source_bundle_id=$2 ORDER BY created_at DESC,id`, tenant, bundle)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := []CompensationPlan{}
	for rows.Next() {
		p, err := readCompensation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (s *Store) replayCompensation(ctx context.Context, in CompensationInput) (CompensationPlan, bool, error) {
	var id, request string
	err := s.pool.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.compensation_plans WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompensationPlan{}, false, nil
	}
	if err != nil {
		return CompensationPlan{}, false, ErrUnavailable
	}
	if request != planning.Hash(in) {
		return CompensationPlan{}, true, ErrConflict
	}
	p, err := s.GetCompensation(ctx, in.TenantID, in.BundleID, id)
	return p, true, err
}
func (s *Store) saveCompensation(ctx context.Context, in CompensationInput, p CompensationPlan, b Bundle) (CompensationPlan, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CompensationPlan{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"compensation", in.TenantID, in.IdempotencyKey})); err != nil {
		return CompensationPlan{}, false, ErrUnavailable
	}
	var id, request string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.compensation_plans WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &request)
	if err == nil {
		if request != planning.Hash(in) {
			return CompensationPlan{}, false, ErrConflict
		}
		prior, err := readCompensation(tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.compensation_plans WHERE tenant_id=$1 AND id=$2`, in.TenantID, id))
		return prior, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CompensationPlan{}, false, ErrUnavailable
	}
	source, err := readWriteDocument[WriteExecution](tx.QueryRow(ctx, `SELECT payload,digest FROM agent_delivery.write_executions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, in.TenantID, in.ExecutionID))
	if err != nil {
		return CompensationPlan{}, false, err
	}
	if source.BundleID != in.BundleID || source.EvidenceDigest() != in.ExpectedExecutionHash || p.SourceExecutionHash != source.EvidenceDigest() {
		return CompensationPlan{}, false, ErrConflict
	}
	if p.State == "READY_FOR_REVIEW" {
		b.Digest = ""
		if !b.Valid() || b.Compensation == nil || b.Compensation.ReportID != p.ID || b.Plan.Hash != p.Plan.Hash {
			return CompensationPlan{}, false, ErrInvalid
		}
		b.Digest = b.digest()
		data, err := json.Marshal(b)
		if err != nil || len(data) > MaxBundleBytes {
			return CompensationPlan{}, false, ErrInvalid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.archives(id,tenant_id,change_id,plan_id,plan_hash,created_at,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, b.ID, b.TenantID, b.ChangeID, b.Plan.ID, b.Plan.Hash, b.CreatedAt, data, b.Digest); err != nil {
			return CompensationPlan{}, false, ErrUnavailable
		}
		p.DerivedBundleID = b.ID
		p.DerivedBundleDigest = b.Digest
	}
	p.Digest = compensationDigest(p)
	data, digest, err := writeDocument(p)
	if err != nil {
		return CompensationPlan{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_delivery.compensation_plans(tenant_id,id,source_bundle_id,source_run_id,derived_bundle_id,idempotency_key,request_hash,payload,digest) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9)`, p.TenantID, p.ID, p.SourceBundleID, p.SourceExecutionID, p.DerivedBundleID, in.IdempotencyKey, planning.Hash(in), data, digest); err != nil || tx.Commit(ctx) != nil {
		return CompensationPlan{}, false, ErrUnavailable
	}
	return p, true, nil
}

// Every fresh compensation approval/dispatch verifies the persisted lineage.
// Existing external outcomes can still be reconciled without initiating writes.
func (s *Store) checkCompensation(ctx context.Context, b Bundle) error {
	if b.Compensation == nil {
		return nil
	}
	origin := b.Compensation
	p, err := s.GetCompensation(ctx, b.TenantID, origin.BundleID, origin.ReportID)
	if err != nil {
		return ErrCorrupt
	}
	if p.State != "READY_FOR_REVIEW" || p.DerivedBundleID != b.ID || p.DerivedBundleDigest != b.Digest || p.SourceBundleDigest != origin.BundleDigest || p.SourceExecutionID != origin.ExecutionID || p.SourceExecutionHash != origin.ExecutionHash || planning.Hash(p.Plan) != planning.Hash(b.Plan) {
		return ErrCorrupt
	}
	source, err := s.Get(ctx, b.TenantID, origin.BundleID)
	if err != nil || source.Compensation != nil || source.Digest != origin.BundleDigest {
		return ErrCorrupt
	}
	r, err := s.GetWriteExecution(ctx, Ref{TenantID: b.TenantID, RunID: origin.ExecutionID})
	if err != nil {
		return err
	}
	if r.EvidenceDigest() != origin.ExecutionHash {
		return ErrDrift
	}
	return validateCompensationSource(source, r)
}
