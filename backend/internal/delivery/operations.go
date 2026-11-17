package delivery

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func (s *Store) GetOperation(ctx context.Context, ref Ref) (Operation, error) {
	return readOperation(s.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 AND id=$3`, ref.TenantID, ref.RunID, ref.OperationID))
}
func (s *Store) ClaimOperation(ctx context.Context, ref Ref, owner string, seconds int) (Operation, error) {
	if owner == "" || seconds < 1 || seconds > 300 {
		return Operation{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Operation{}, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, err := readRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID))
	if err != nil {
		return Operation{}, err
	}
	if r.State != "RUNNING" {
		return Operation{}, ErrApproval
	}
	a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, r.ApprovalID))
	if err != nil {
		return Operation{}, err
	}
	if a.State != "CONSUMED" || a.ConsumedBy != r.ID {
		return Operation{}, ErrApproval
	}
	o, err := readOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 AND id=$3 FOR UPDATE`, ref.TenantID, ref.RunID, ref.OperationID))
	if err != nil {
		return o, err
	}
	if o.State == "SUCCEEDED" {
		return o, nil
	}
	if o.State == "FAILED" || o.State == "BLOCKED" {
		return o, ErrApproval
	}
	var busy bool
	if tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false) FROM agent_delivery.operations WHERE tenant_id=$1 AND id=$2`, ref.TenantID, o.ID).Scan(&busy) != nil {
		return o, ErrUnavailable
	}
	if busy {
		return o, ErrBusy
	}
	o, err = readOperation(tx.QueryRow(ctx, `UPDATE agent_delivery.operations SET state='RUNNING',owner=$3,fence=fence+1,attempts=attempts+1,lease_until=clock_timestamp()+make_interval(secs=>$4),error_code='' WHERE tenant_id=$1 AND id=$2 RETURNING `+operationColumns, ref.TenantID, o.ID, owner, seconds))
	if err != nil {
		return o, err
	}
	if tx.Commit(ctx) != nil {
		return Operation{}, ErrUnavailable
	}
	return o, nil
}

// SimulateEffect is the only "write" implementation in D4.2. It commits a
// synthetic receipt to its own transaction; it cannot call a company connector.
func (s *Store) SimulateEffect(ctx context.Context, ref Ref, fence int64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, err := readRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID))
	if err != nil {
		return false, err
	}
	if r.State != "RUNNING" {
		return false, ErrApproval
	}
	a, err := readApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM agent_delivery.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, r.ApprovalID))
	if err != nil {
		return false, err
	}
	if a.State != "CONSUMED" || a.ConsumedBy != r.ID {
		return false, ErrApproval
	}
	o, err := readOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 AND id=$3 FOR UPDATE`, ref.TenantID, r.ID, ref.OperationID))
	if err != nil {
		return false, err
	}
	var active bool
	if tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false) FROM agent_delivery.operations WHERE tenant_id=$1 AND id=$2`, ref.TenantID, o.ID).Scan(&active) != nil {
		return false, ErrUnavailable
	}
	if o.Fence != fence || o.State != "RUNNING" || !active {
		return false, ErrLeaseLost
	}
	result, err := tx.Exec(ctx, `INSERT INTO agent_delivery.simulation_receipts(operation_id,tenant_id,action_hash,receipt_id,fence,mode) SELECT $1,$2,$3,$4,$5,'SIMULATION' WHERE $6::timestamptz>clock_timestamp() AND $7::timestamptz>clock_timestamp() ON CONFLICT(operation_id) DO NOTHING`, o.ID, ref.TenantID, o.ActionHash, "receipt-"+o.ID, fence, a.ExpiresAt, o.LeaseUntil)
	if err != nil {
		return false, ErrUnavailable
	}
	if result.RowsAffected() == 0 {
		var exists, authorized bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_delivery.simulation_receipts WHERE tenant_id=$1 AND operation_id=$2),$3::timestamptz>clock_timestamp()`, ref.TenantID, o.ID, a.ExpiresAt).Scan(&exists, &authorized) != nil {
			return false, ErrUnavailable
		}
		if !exists {
			if !authorized {
				return false, ErrApproval
			}
			return false, ErrLeaseLost
		}
	}
	if tx.Commit(ctx) != nil {
		return false, ErrUnavailable
	}
	return result.RowsAffected() == 1, nil
}

// A committed receipt proves the simulator's outcome even after expiry/revoke.
// Recording that fact must not require permission to initiate another effect.
func (s *Store) ReconcileOperation(ctx context.Context, ref Ref) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	o, err := readOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 AND id=$3 FOR UPDATE`, ref.TenantID, ref.RunID, ref.OperationID))
	if err != nil {
		return false, err
	}
	if o.State == "SUCCEEDED" {
		return true, nil
	}
	var hash, receipt string
	err = tx.QueryRow(ctx, `SELECT action_hash,receipt_id FROM agent_delivery.simulation_receipts WHERE tenant_id=$1 AND operation_id=$2 AND mode='SIMULATION'`, ref.TenantID, o.ID).Scan(&hash, &receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	if hash != o.ActionHash {
		return false, ErrCorrupt
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_delivery.operations SET state='SUCCEEDED',receipt_id=$3,completed_at=clock_timestamp(),lease_until=NULL,error_code='' WHERE tenant_id=$1 AND id=$2`, ref.TenantID, o.ID, receipt); err != nil {
		return false, ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return false, ErrUnavailable
	}
	return true, nil
}
func (s *Store) MarkUnknown(ctx context.Context, ref Ref, fence int64) error {
	r, err := s.pool.Exec(ctx, `UPDATE agent_delivery.operations SET state='UNKNOWN',error_code='simulated_response_lost',lease_until=NULL WHERE tenant_id=$1 AND run_id=$2 AND id=$3 AND fence=$4 AND state='RUNNING'`, ref.TenantID, ref.RunID, ref.OperationID, fence)
	if err != nil {
		return ErrUnavailable
	}
	if r.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) FailOperation(ctx context.Context, ref Ref, fence int64, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Match lock order used by Claim/SimulateEffect, then reject stale completion.
	if _, err = tx.Exec(ctx, `SELECT id FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID); err != nil {
		return ErrUnavailable
	}
	result, err := tx.Exec(ctx, `UPDATE agent_delivery.operations SET state='FAILED',error_code=$5,completed_at=clock_timestamp(),lease_until=NULL WHERE tenant_id=$1 AND run_id=$2 AND id=$3 AND fence=$4 AND state='RUNNING'`, ref.TenantID, ref.RunID, ref.OperationID, fence, code)
	if err != nil {
		return ErrUnavailable
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_delivery.simulations SET state='FAILED',error_code=$3,completed_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND state='RUNNING'`, ref.TenantID, ref.RunID, code); err != nil {
		return ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) BlockSimulation(ctx context.Context, ref Ref, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, err := readRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_delivery.simulations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, ref.TenantID, ref.RunID))
	if err != nil {
		return err
	}
	if r.State != "QUEUED" && r.State != "RUNNING" {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_delivery.approvals SET state='INVALIDATED',version=version+1,error_code=$3 WHERE tenant_id=$1 AND id=$2 AND state IN ('APPROVED','CONSUMED')`, ref.TenantID, r.ApprovalID, code); err != nil {
		return ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_delivery.simulations SET state='BLOCKED',error_code=$3,completed_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, ref.TenantID, r.ID, code); err != nil {
		return ErrUnavailable
	}
	if ref.OperationID != "" {
		if _, err = tx.Exec(ctx, `UPDATE agent_delivery.operations SET state='BLOCKED',error_code=$4,lease_until=NULL,completed_at=clock_timestamp() WHERE tenant_id=$1 AND run_id=$2 AND id=$3 AND state IN ('NOT_STARTED','RUNNING','UNKNOWN') AND NOT EXISTS(SELECT 1 FROM agent_delivery.simulation_receipts WHERE tenant_id=$1 AND operation_id=$3)`, ref.TenantID, r.ID, ref.OperationID, code); err != nil {
			return ErrUnavailable
		}
	}
	if tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) FinishSimulation(ctx context.Context, ref Ref) (string, error) {
	r, err := s.GetSimulation(ctx, ref)
	if err != nil {
		return "", err
	}
	if r.State == "SIMULATED" || r.State == "BLOCKED" || r.State == "FAILED" {
		return r.State, nil
	}
	result, err := s.pool.Exec(ctx, `UPDATE agent_delivery.simulations SET state='SIMULATED',completed_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND state='RUNNING' AND NOT EXISTS(SELECT 1 FROM agent_delivery.operations WHERE tenant_id=$1 AND run_id=$2 AND state<>'SUCCEEDED')`, ref.TenantID, ref.RunID)
	if err != nil {
		return "", ErrUnavailable
	}
	if result.RowsAffected() != 1 {
		return "", ErrBusy
	}
	return "SIMULATED", nil
}
