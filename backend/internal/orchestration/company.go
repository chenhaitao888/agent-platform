package orchestration

import (
	"context"
	"time"

	"agent-platform/backend/internal/delivery"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const CompanyWorkflowName = "delivery.company.v1"

type CompanyInput struct {
	delivery.Ref
	Tracking bool `json:"tracking,omitempty"`
}

// Existing write workflow histories are untouched. This workflow has a short
// authorized mutation phase followed by durable, read-only company tracking.
func CompanyWorkflow(ctx workflow.Context, in CompanyInput) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 90 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	var ids []string
	if err := workflow.ExecuteActivity(ctx, "write.begin", in.Ref).Get(ctx, &ids); err != nil {
		return "", err
	}
	if !in.Tracking {
		canSubmit := true
		for _, id := range ids {
			ref := in.Ref
			ref.OperationID = id
			var next bool
			err := workflow.ExecuteActivity(ctx, "write.step", ref).Get(ctx, &next)
			if err != nil {
				if blockErr := workflow.ExecuteActivity(ctx, "write.block", in.Ref).Get(ctx, nil); blockErr != nil {
					return "", blockErr
				}
			}
			if err != nil || !next {
				canSubmit = false
				break
			}
		}
		if canSubmit {
			for _, id := range ids {
				ref := in.Ref
				ref.OperationID = id
				var next bool
				err := workflow.ExecuteActivity(ctx, "company.submit", ref).Get(ctx, &next)
				if err != nil || !next {
					break
				}
			}
		}
	}
	for poll := 0; poll < 100; poll++ {
		done := true
		for _, id := range ids {
			ref := in.Ref
			ref.OperationID = id
			var finished bool
			if err := workflow.ExecuteActivity(ctx, "company.observe", ref).Get(ctx, &finished); err != nil {
				return "", err
			}
			done = done && finished
		}
		var state string
		if err := workflow.ExecuteActivity(ctx, "write.finish", in.Ref).Get(ctx, &state); err != nil {
			return "", err
		}
		if done {
			return state, nil
		}
		if err := workflow.Sleep(ctx, 30*time.Second); err != nil {
			return "", err
		}
	}
	in.Tracking = true
	return "", workflow.NewContinueAsNewError(ctx, CompanyWorkflowName, in)
}
func (a writeActivities) CompanySubmit(ctx context.Context, ref delivery.Ref) (bool, error) {
	next, err := a.service.CompanySubmit(ctx, ref)
	return next, safeWrite(err)
}
func (a writeActivities) CompanyObserve(ctx context.Context, ref delivery.Ref) (bool, error) {
	done, err := a.service.CompanyObserve(ctx, ref)
	return done, safeWrite(err)
}
