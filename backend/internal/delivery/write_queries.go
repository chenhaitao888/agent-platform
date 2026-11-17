package delivery

import "context"

func (s *Store) ListWriteApprovals(ctx context.Context, tenant, bundle string) ([]WriteApproval, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload,digest FROM agent_delivery.write_approvals WHERE tenant_id=$1 AND bundle_id=$2 ORDER BY (payload->>'createdAt')::timestamptz DESC,id`, tenant, bundle)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := []WriteApproval{}
	for rows.Next() {
		item, err := readWriteDocument[WriteApproval](rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (s *Store) ListWriteExecutions(ctx context.Context, tenant, bundle string) ([]WriteExecution, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload,digest FROM agent_delivery.write_executions WHERE tenant_id=$1 AND bundle_id=$2 ORDER BY (payload->>'createdAt')::timestamptz DESC,id`, tenant, bundle)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := []WriteExecution{}
	for rows.Next() {
		item, err := readWriteDocument[WriteExecution](rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
