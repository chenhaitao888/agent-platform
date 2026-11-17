package resourcewrite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
)

type remainingReader struct{ state planning.ConfigState }

func (r *remainingReader) Describe() planning.Descriptor {
	d := descriptor()
	d.ContractVersion = "config-read.v1"
	return d
}
func (r *remainingReader) ReadConfig(_ context.Context, target planning.Target) (planning.ConfigState, error) {
	if target != r.state.Target {
		return planning.ConfigState{}, planning.ErrUnavailable
	}
	return planning.Clone(r.state), nil
}
func TestRevalidationUsesVerifiedPrefixWithoutMutatingSealedPlan(t *testing.T) {
	plan := testPlan()
	a := plan.Actions[0]
	plan.Observations = []planning.Observation{{Kind: a.Kind, Target: a.Target, Descriptor: a.Descriptor, Version: a.ResourceVersion}}
	plan.Hash = ""
	plan.Hash = planning.Hash(plan)
	i, err := resourcewrite.Prepare(plan, 0, "first", descriptor(), time.Now().Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := completed(t, i)
	r := &remainingReader{state: planning.ConfigState{Target: a.Target, Version: done.Receipt.ResourceVersion, SaveMode: a.Effect, ConditionalUpdates: true, Items: []planning.ConfigItem{{Key: a.Key, Version: done.Receipt.ItemVersion, Value: *i.Value}}}}
	planner, err := planning.NewPlanner(map[string]planning.ConfigReader{"config": r}, nil, planning.DiffAnalyzer{})
	if err != nil {
		t.Fatal(err)
	}
	before := planning.Hash(plan)
	prefix := []resourcewrite.Completed{done}
	if err := resourcewrite.RevalidateRemaining(context.Background(), planner, plan, prefix); err != nil {
		t.Fatal(err)
	}
	if planning.Hash(plan) != before {
		t.Fatal("mutated sealed plan")
	}
	if err := resourcewrite.RevalidateRemaining(context.Background(), planner, plan, nil); !errors.Is(err, planning.ErrDrift) {
		t.Fatal("absorbed new version without prefix")
	}
	for _, mode := range []string{"foreign-change", "missing-proof", "wrong-action", "missing-observation", "changed-descriptor", "changed-effect"} {
		t.Run(mode, func(t *testing.T) {
			p := planning.Clone(plan)
			completed := planning.Clone(prefix)
			state := planning.Clone(r.state)
			switch mode {
			case "foreign-change":
				r.state.Version = "v3"
			case "missing-proof":
				completed[0].Verification.State = "DRIFTED"
			case "wrong-action":
				completed[0].Intent.ActionID = "unrelated"
			case "missing-observation":
				p.Observations = nil
			case "changed-descriptor":
				p.Observations[0].Descriptor.ConnectionVersion = "2"
			case "changed-effect":
				r.state.SaveMode = "immediate"
			}
			p.Hash = ""
			p.Hash = planning.Hash(p)
			if err := resourcewrite.RevalidateRemaining(context.Background(), planner, p, completed); err == nil {
				t.Fatal("unsafe revalidation")
			}
			r.state = state
		})
	}
}
