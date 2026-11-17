package resourcewrite

import (
	"context"

	"agent-platform/backend/internal/planning"
)

// RevalidateRemaining checks all observed resources, including ones with no
// remaining actions, against the original baseline advanced only by this run's
// verified prefix. Repository refs and current permissions are the policy's job.
// It never mutates or re-hashes the sealed plan as a new approved plan.
func RevalidateRemaining(ctx context.Context, reader *planning.Planner, plan planning.Plan, prefix []Completed) error {
	copy := plan
	copy.Hash = ""
	if reader == nil || plan.SchemaVersion != "change-plan.v1" || plan.Hash != planning.Hash(copy) || plan.State != "READY_FOR_REVIEW" || len(plan.Issues) != 0 || len(prefix) > len(plan.Actions) {
		return ErrInvalid
	}
	if len(prefix) > 0 {
		last := prefix[len(prefix)-1]
		i, err := Prepare(plan, len(prefix)-1, last.Intent.OperationID, last.Intent.Writer, last.Intent.ExpiresAt, prefix[:len(prefix)-1])
		if err != nil || planning.Hash(i) != planning.Hash(last.Intent) || !last.Verification.ValidFor(i, last.Receipt) {
			return ErrInvalid
		}
	}
	type resource struct {
		Kind   string
		Target planning.Target
	}
	versions := map[resource]string{}
	descriptors := map[resource]planning.Descriptor{}
	for _, observation := range plan.Observations {
		key := resource{observation.Kind, observation.Target}
		if _, exists := versions[key]; exists {
			return ErrInvalid
		}
		versions[key] = observation.Version
		descriptors[key] = observation.Descriptor
	}
	for _, action := range plan.Actions {
		key := resource{action.Kind, action.Target}
		if versions[key] != action.ResourceVersion || descriptors[key] != action.Descriptor {
			return ErrInvalid
		}
	}
	for _, complete := range prefix {
		versions[resource{complete.Intent.Kind, complete.Intent.Target}] = complete.Receipt.ResourceVersion
	}
	remaining := planning.Clone(plan)
	for n := range remaining.Observations {
		o := &remaining.Observations[n]
		o.Version = versions[resource{o.Kind, o.Target}]
	}
	remaining.Actions = remaining.Actions[len(prefix):]
	for n := range remaining.Actions {
		a := &remaining.Actions[n]
		a.ResourceVersion = versions[resource{a.Kind, a.Target}]
	}
	return reader.Revalidate(ctx, remaining)
}
