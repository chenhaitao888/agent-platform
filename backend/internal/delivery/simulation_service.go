package delivery

import (
	"context"
	"errors"
)

func (s *SimulationService) Approve(ctx context.Context, in ApprovalInput) (Approval, bool, error) {
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return Approval{}, false, err
	}
	stamp, err := s.Policy.Authorize(ctx, in.Subject, b, "approve_simulation")
	if err != nil {
		return Approval{}, false, err
	}
	in.PolicyHash = stamp
	return s.Store.Approve(ctx, in)
}
func (s *SimulationService) Start(ctx context.Context, in StartInput) (Simulation, bool, error) {
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return Simulation{}, false, err
	}
	if _, err := s.Policy.Authorize(ctx, in.Subject, b, "simulate"); err != nil {
		return Simulation{}, false, err
	}
	return s.Store.StartSimulation(ctx, in)
}
func (s *SimulationService) Revoke(ctx context.Context, in RevokeInput) (Approval, error) {
	b, err := s.Store.Get(ctx, in.TenantID, in.BundleID)
	if err != nil {
		return Approval{}, err
	}
	if _, err := s.Policy.Authorize(ctx, in.Subject, b, "revoke_simulation"); err != nil {
		return Approval{}, err
	}
	return s.Store.Revoke(ctx, in)
}
func (s *SimulationService) check(ctx context.Context, r Simulation) (string, error) {
	b, err := s.Store.Get(ctx, r.TenantID, r.BundleID)
	if err != nil {
		return "", err
	}
	a, err := s.Store.GetApproval(ctx, r.TenantID, r.BundleID, r.ApprovalID)
	if err != nil {
		return "", err
	}
	if a.State == "EXPIRED" {
		return "approval_expired", nil
	}
	if a.State == "REVOKED" {
		return "approval_revoked", nil
	}
	if a.State != "APPROVED" && (a.State != "CONSUMED" || a.ConsumedBy != r.ID) {
		return "approval_inactive", nil
	}
	stamp, err := s.Policy.Authorize(ctx, a.Subject, b, "approve_simulation")
	if err != nil {
		return "authorization_changed", nil
	}
	if stamp != a.PolicyHash {
		return "policy_changed", nil
	}
	if _, err := s.Policy.Authorize(ctx, r.Subject, b, "simulate"); err != nil {
		return "authorization_changed", nil
	}
	if err := s.Policy.Revalidate(ctx, b); err != nil {
		if errors.Is(err, ErrDrift) || errors.Is(err, ErrPolicy) {
			return "resource_or_repository_drift", nil
		}
		return "", ErrUnavailable
	}
	return "", nil
}
func (s *SimulationService) Begin(ctx context.Context, ref Ref) ([]string, error) {
	r, err := s.Store.GetSimulation(ctx, ref)
	if err != nil {
		return nil, err
	}
	if r.State == "QUEUED" {
		code, err := s.check(ctx, r)
		if err != nil {
			return nil, err
		}
		if code != "" {
			return []string{}, s.Store.BlockSimulation(ctx, ref, code)
		}
	}
	ids, err := s.Store.BeginSimulation(ctx, ref)
	if errors.Is(err, ErrApproval) {
		return []string{}, s.Store.BlockSimulation(ctx, ref, "approval_inactive")
	}
	return ids, err
}
func (s *SimulationService) Step(ctx context.Context, ref Ref) (bool, error) {
	r, err := s.Store.GetSimulation(ctx, ref)
	if err != nil {
		return false, err
	}
	// Reconciliation precedes new authorization checks: a lost response may
	// belong to an effect committed before a later revocation or expiration.
	found, err := s.Store.ReconcileOperation(ctx, ref)
	if err != nil {
		return false, err
	}
	if found {
		return true, nil
	}
	if r.State != "RUNNING" {
		return false, nil
	}
	code, err := s.check(ctx, r)
	if err != nil {
		return false, err
	}
	if code != "" {
		return false, s.Store.BlockSimulation(ctx, ref, code)
	}
	o, err := s.Store.ClaimOperation(ctx, ref, s.Owner, s.LeaseSeconds)
	if errors.Is(err, ErrApproval) {
		return false, s.Store.BlockSimulation(ctx, ref, "approval_inactive")
	}
	if err != nil {
		return false, err
	}
	if o.State == "SUCCEEDED" {
		return true, nil
	}
	if r.Scenario == "fail_before" && o.Ordinal == min(1, len(r.Operations)-1) {
		return false, s.Store.FailOperation(ctx, ref, o.Fence, "simulated_failure")
	}
	created, err := s.Store.SimulateEffect(ctx, ref, o.Fence)
	if errors.Is(err, ErrApproval) {
		return false, s.Store.BlockSimulation(ctx, ref, "approval_inactive")
	}
	if err != nil {
		return false, err
	}
	if created && r.Scenario == "response_loss" {
		if err := s.Store.MarkUnknown(ctx, ref, o.Fence); err != nil {
			return false, err
		}
		return false, ErrRetry
	}
	return s.Store.ReconcileOperation(ctx, ref)
}
