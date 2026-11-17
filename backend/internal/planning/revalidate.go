package planning

import (
	"context"
	"errors"
)

var ErrDrift = errors.New("planning observation or connector contract changed")

func (p *Planner) Revalidate(ctx context.Context, plan Plan) error {
	cache := map[resourceKey]observed{}
	for _, observation := range plan.Observations {
		if !p.Registered(observation.Kind, observation.Target.ConnectorRef) {
			return ErrDrift
		}
		key := resourceKey{kind: observation.Kind, target: observation.Target}
		state := p.observe(ctx, key.kind, key.target)
		if state.err != nil {
			return ErrUnavailable
		}
		version := state.config.Version
		if key.kind == "experiment" {
			version = state.experiments.Version
		}
		if state.descriptor != observation.Descriptor || version != observation.Version {
			return ErrDrift
		}
		cache[key] = state
	}
	for _, action := range plan.Actions {
		state, ok := cache[resourceKey{kind: action.Kind, target: action.Target}]
		if !ok {
			return ErrDrift
		}
		fresh := Action{Kind: action.Kind, Key: action.Key, Target: action.Target, Descriptor: state.descriptor, Proposals: Clone(action.Proposals)}
		if len(fresh.Proposals) == 0 {
			return ErrDrift
		}
		check := Plan{Issues: []Issue{}}
		switch action.Kind {
		case "config":
			configAction(&check, &fresh, state.config)
		case "experiment":
			experimentAction(&check, &fresh, state.experiments)
		default:
			return ErrDrift
		}
		prior := action
		prior.ID = ""
		prior.Hash = ""
		if len(check.Issues) != 0 || Hash(fresh) != Hash(prior) {
			return ErrDrift
		}
	}
	return nil
}
