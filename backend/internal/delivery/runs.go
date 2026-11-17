package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"

	"agent-platform/backend/internal/planning"
	"github.com/jackc/pgx/v5"
)

const runColumns = `id,tenant_id,bundle_id,approval_id,subject,scenario,state,error_code,created_at,completed_at,workflow_id`
const operationColumns = `id,tenant_id,run_id,ordinal,action_id,action_hash,state,attempts,fence,owner,lease_until,error_code,receipt_id,completed_at`

func readRun(row pgx.Row) (Simulation, error) {
	var r Simulation
	r.Operations = []Operation{}
	err := row.Scan(&r.ID, &r.TenantID, &r.BundleID, &r.ApprovalID, &r.Subject, &r.Scenario, &r.State, &r.ErrorCode, &r.CreatedAt, &r.CompletedAt, &r.WorkflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, ErrUnavailable
	}
	return r, nil
}
func readOperation(row pgx.Row) (Operation, error) {
	var o Operation
	err := row.Scan(&o.ID, &o.TenantID, &o.RunID, &o.Ordinal, &o.ActionID, &o.ActionHash, &o.State, &o.Attempts, &o.Fence, &o.Owner, &o.LeaseUntil, &o.ErrorCode, &o.ReceiptID, &o.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, ErrUnavailable
	}
	return o, nil
}
func (s *Store) StartSimulation(ctx context.Context, in StartInput) (Simulation, bool, error) {
	if in.Scenario == "" {
		in.Scenario = "success"
	}
	if !validKey(in.IdempotencyKey) || in.Subject == "" || !slices.Contains([]string{"success", "response_loss", "fail_before"}, in.Scenario) {
		return Simulation{}, false, ErrInvalid
	}
	b, err := s.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return Simulation{}, false, err
	}
	if b.Digest != in.ExpectedDigest {
		return Simulation{}, false, ErrConflict
	}
	if !eligible(b) {
		return Simulation{}, false, ErrUnready
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Simulation{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	digest := planning.Hash(in)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, planning.Hash([]string{"simulate", in.TenantID, in.IdempotencyKey})); err != nil {
		return Simulation{}, false, ErrUnavailable
	}
	var id, prior string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM agent_delivery.simulations WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&id, &prior)
	if err == nil {
		if prior != digest {
			return Simulation{}, false, ErrConflict
		}
		r, err := readRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2`, in.TenantID, id))
		return r, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Simulation{}, false, ErrUnavailable
	}
	a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND bundle_id=$2 AND id=$3 FOR UPDATE`, in.TenantID, in.BundleID, in.ApprovalID))
	if err != nil {
		return Simulation{}, false, err
	}
	if a.State != "APPROVED" || a.BundleDigest != b.Digest || a.PlanHash != b.Plan.Hash {
		return Simulation{}, false, ErrApproval
	}
	var used bool
	if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_delivery.simulations WHERE tenant_id=$1 AND approval_id=$2)`, in.TenantID, a.ID).Scan(&used) != nil {
		return Simulation{}, false, ErrUnavailable
	}
	if used {
		return Simulation{}, false, ErrApproval
	}
	id = "simulation-" + rand.Text()
	r, err := readRun(tx.QueryRow(ctx, `INSERT INTO agent_delivery.simulations(id,tenant_id,bundle_id,approval_id,subject,scenario,state,workflow_id,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,'QUEUED',$7,$8,$9) RETURNING `+runColumns, id, in.TenantID, in.BundleID, a.ID, in.Subject, in.Scenario, "delivery-"+id, in.IdempotencyKey, digest))
	if err != nil {
		return Simulation{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_delivery.outbox(id,tenant_id,run_id,kind) VALUES($1,$2,$3,'simulation.requested.v1')`, "outbox-"+rand.Text(), in.TenantID, id); err != nil {
		return Simulation{}, false, ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return Simulation{}, false, ErrUnavailable
	}
	return r, true, nil
}
func (s *Store) GetSimulation(ctx context.Context, ref Ref) (Simulation, error) {
	r, err := readRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2`, ref.TenantID, ref.RunID))
	if err != nil {
		return r, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+operationColumns+` FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 ORDER BY ordinal`, ref.TenantID, ref.RunID)
	if err != nil {
		return Simulation{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		o, err := readOperation(rows)
		if err != nil {
			return Simulation{}, err
		}
		r.Operations = append(r.Operations, o)
	}
	if rows.Err() != nil {
		return Simulation{}, ErrUnavailable
	}
	return r, nil
}
func (s *Store) ListSimulations(ctx context.Context, tenant, bundle string) ([]Simulation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND bundle_id=$2 ORDER BY created_at DESC,id`, tenant, bundle)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []Simulation{}
	for rows.Next() {
		r, err := readRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	rows.Close()
	for i := range out {
		full, err := s.GetSimulation(ctx, Ref{TenantID: tenant, RunID: out[i].ID})
		if err != nil {
			return nil, err
		}
		out[i] = full
	}
	return out, nil
}
func (s *Store) BeginSimulation(ctx context.Context, ref Ref) ([]string, error) {
	r, err := s.GetSimulation(ctx, ref)
	if err != nil {
		return nil, err
	}
	b, err := s.Get(ctx, ref.TenantID, r.BundleID)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, err = readRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID))
	if err != nil {
		return nil, err
	}
	if r.State == "QUEUED" {
		a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, r.ApprovalID))
		if err != nil {
			return nil, err
		}
		if a.State != "APPROVED" || a.BundleDigest != b.Digest {
			return nil, ErrApproval
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_delivery.approvals SET state='CONSUMED',version=version+1,consumed_by=$3 WHERE tenant_id=$1 AND id=$2`, ref.TenantID, a.ID, r.ID); err != nil {
			return nil, ErrUnavailable
		}
		for i, action := range b.Plan.Actions {
			if _, err = tx.Exec(ctx, `INSERT INTO agent_delivery.operations(id,tenant_id,run_id,ordinal,action_id,action_hash,state) VALUES($1,$2,$3,$4,$5,$6,'NOT_STARTED')`, "operation-"+rand.Text(), ref.TenantID, r.ID, i, action.ID, action.Hash); err != nil {
				return nil, ErrUnavailable
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_delivery.simulations SET state='RUNNING' WHERE tenant_id=$1 AND id=$2`, ref.TenantID, r.ID); err != nil {
			return nil, ErrUnavailable
		}
	}
	ids := []string{}
	if r.State == "QUEUED" || r.State == "RUNNING" {
		rows, err := tx.Query(ctx, `SELECT id FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 ORDER BY ordinal`, ref.TenantID, r.ID)
		if err != nil {
			return nil, ErrUnavailable
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) != nil {
				rows.Close()
				return nil, ErrUnavailable
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	if tx.Commit(ctx) != nil {
		return nil, ErrUnavailable
	}
	return ids, nil
}
