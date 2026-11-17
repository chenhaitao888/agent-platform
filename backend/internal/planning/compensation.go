package planning

import "context"

// ReadConfigState exposes the existing validated GET reader without invoking an
// analyzer. Compensation preparation must never trigger model inference.
func (p *Planner) ReadConfigState(ctx context.Context, target Target) (Descriptor, ConfigState, error) {
	if !p.Registered("config", target.ConnectorRef) {
		return Descriptor{}, ConfigState{}, ErrUnavailable
	}
	state := p.observe(ctx, "config", target)
	return state.descriptor, state.config, state.err
}

// RestoreConfig builds a typed inverse update and reuses normal value, secret
// reference, capability and constraint checks. Receipt ownership is the caller's
// responsibility; this function is not an authorization or execution method.
func RestoreConfig(original Action, descriptor Descriptor, state ConfigState) (Action, error) {
	if original.Kind != "config" || original.Operation != "config.update" || original.CurrentValue == nil || !original.CurrentValue.Valid() || len(original.Proposals) == 0 || descriptor != original.Descriptor || !ValidConfigState(state, original.Target) {
		return Action{}, ErrInvalid
	}
	proposals := Clone(original.Proposals)
	for n := range proposals {
		proposals[n].Value = Clone(original.CurrentValue)
		proposals[n].Experiment = nil
		proposals[n].Evidence = []Evidence{{Source: "compensation-receipt", ServiceID: proposals[n].ServiceID, Reason: "restore value preceding the recorded operation"}}
	}
	a := Action{Kind: "config", Key: original.Key, Target: original.Target, Descriptor: descriptor, Proposals: proposals}
	check := Plan{Issues: []Issue{}}
	configAction(&check, &a, state)
	if len(check.Issues) != 0 || a.Operation != "config.update" {
		return Action{}, ErrInvalid
	}
	return a, nil
}
