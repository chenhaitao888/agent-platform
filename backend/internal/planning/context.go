package planning

import (
	"context"
	"time"
)

func (p *Planner) cachedObservation(ctx context.Context, plan *Plan, key resourceKey, cache map[resourceKey]observed) observed {
	if state, ok := cache[key]; ok {
		return state
	}
	state := p.observe(ctx, key.kind, key.target)
	cache[key] = state
	if state.err == nil {
		version := state.config.Version
		if key.kind == "experiment" {
			version = state.experiments.Version
		}
		plan.Observations = append(plan.Observations, Observation{Kind: key.kind, Target: key.target, Descriptor: state.descriptor, Version: version, ObservedAt: time.Now().UTC()})
	}
	return state
}

func (p *Planner) analysisContext(ctx context.Context, plan *Plan, service ServiceInput, cache map[resourceKey]observed) []ResourceContext {
	result := []ResourceContext{}
	for _, kind := range []string{"config", "experiment"} {
		summary := ResourceContext{Kind: kind, Status: "UNBOUND", Items: []ResourceItem{}}
		binding := service.Bindings.Config
		if kind == "experiment" {
			binding = service.Bindings.Experiment
		}
		if binding != nil && p.Registered(kind, binding.ConnectorRef) {
			target := Target{TenantID: plan.TenantID, Environment: plan.Environment, ConnectorRef: binding.ConnectorRef, ResourceRef: binding.ResourceRef}
			state := p.cachedObservation(ctx, plan, resourceKey{kind: kind, target: target}, cache)
			summary.Status = "UNAVAILABLE"
			if state.err != nil {
				addIssue(plan, "platform_read_failed", service.Source.ServiceID, kind, "")
			} else {
				summary.Status = "AVAILABLE"
				if kind == "config" {
					summary.Version = state.config.Version
					for _, item := range state.config.Items {
						summary.Items = append(summary.Items, ResourceItem{Key: item.Key, Type: item.Value.Type})
					}
				} else {
					summary.Version = state.experiments.Version
					for _, item := range state.experiments.Items {
						summary.Items = append(summary.Items, ResourceItem{Key: item.Key, State: item.State})
					}
				}
			}
		}
		result = append(result, summary)
	}
	return result
}
