package delivery

import "context"

func (s *Store) ClaimWriteOutbox(ctx context.Context, owner string, limit int) ([]OutboxMessage, error) {
	if owner == "" || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `WITH candidates AS (SELECT id FROM agent_delivery.write_outbox WHERE delivered_at IS NULL AND available_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT $2), claimed AS (UPDATE agent_delivery.write_outbox o SET owner=$1,fence=fence+1,attempts=attempts+1,lease_until=clock_timestamp()+interval '30 seconds' FROM candidates c WHERE o.id=c.id RETURNING o.*) SELECT c.id,c.tenant_id,c.run_id,r.workflow_id,c.owner,c.fence,c.attempts FROM claimed c JOIN agent_delivery.write_executions r ON r.tenant_id=c.tenant_id AND r.id=c.run_id`, owner, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []OutboxMessage{}
	for rows.Next() {
		var m OutboxMessage
		if rows.Scan(&m.ID, &m.TenantID, &m.RunID, &m.WorkflowID, &m.Owner, &m.Fence, &m.Attempts) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
func (s *Store) SetWriteOutboxDelivered(ctx context.Context, m OutboxMessage) error {
	result, err := s.pool.Exec(ctx, `UPDATE agent_delivery.write_outbox SET delivered_at=clock_timestamp(),lease_until=NULL WHERE tenant_id=$1 AND id=$2 AND owner=$3 AND fence=$4 AND delivered_at IS NULL`, m.TenantID, m.ID, m.Owner, m.Fence)
	if err != nil {
		return ErrUnavailable
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) ReleaseWriteOutbox(ctx context.Context, m OutboxMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_delivery.write_outbox SET lease_until=NULL,available_at=clock_timestamp()+interval '1 second' WHERE tenant_id=$1 AND id=$2 AND owner=$3 AND fence=$4 AND delivered_at IS NULL`, m.TenantID, m.ID, m.Owner, m.Fence)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
