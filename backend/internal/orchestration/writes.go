// Package orchestration uses Temporal for workflow retries/recovery. Only IDs
// cross the history boundary; activities load immutable input from PostgreSQL.
package orchestration

import (
	"context"
	"errors"
	"time"

	"agent-platform/backend/internal/delivery"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const WriteWorkflowName = "delivery.write.v1"

func WriteWorkflow(ctx workflow.Context, ref delivery.Ref) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 5 * time.Second}})
	var ids []string
	err := workflow.ExecuteActivity(ctx, "write.begin", ref).Get(ctx, &ids)
	if err == nil {
		for _, id := range ids {
			step := ref
			step.OperationID = id
			var next bool
			err = workflow.ExecuteActivity(ctx, "write.step", step).Get(ctx, &next)
			if err != nil || !next {
				break
			}
		}
	}
	if err != nil {
		if blockErr := workflow.ExecuteActivity(ctx, "write.block", ref).Get(ctx, nil); blockErr != nil {
			return "", blockErr
		}
	}
	var state string
	err = workflow.ExecuteActivity(ctx, "write.finish", ref).Get(ctx, &state)
	return state, err
}

type writeActivities struct{ service *delivery.WriteService }

func safeWrite(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, delivery.ErrUnavailable) || errors.Is(err, delivery.ErrBusy) || errors.Is(err, delivery.ErrLeaseLost) || errors.Is(err, delivery.ErrRetry) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("write activity awaits retry or reconciliation")
	}
	return temporal.NewNonRetryableApplicationError("saved write input cannot continue", "invalid_write", nil)
}
func (a writeActivities) Begin(ctx context.Context, ref delivery.Ref) ([]string, error) {
	ids, err := a.service.Begin(ctx, ref)
	return ids, safeWrite(err)
}
func (a writeActivities) Step(ctx context.Context, ref delivery.Ref) (bool, error) {
	next, err := a.service.Step(ctx, ref)
	return next, safeWrite(err)
}
func (a writeActivities) Block(ctx context.Context, ref delivery.Ref) error {
	return safeWrite(a.service.Block(ctx, ref, "invalid_saved_input"))
}
func (a writeActivities) Finish(ctx context.Context, ref delivery.Ref) (string, error) {
	state, err := a.service.Finish(ctx, ref)
	return state, safeWrite(err)
}

func DispatchWritesOnce(ctx context.Context, store *delivery.Store, c client.Client, queue, owner string) error {
	jobs, err := store.ClaimWriteOutbox(ctx, owner, 20)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := c.ExecuteWorkflow(call, client.StartWorkflowOptions{ID: job.WorkflowID, TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, WorkflowExecutionErrorWhenAlreadyStarted: true}, WriteWorkflowName, delivery.Ref{TenantID: job.TenantID, RunID: job.RunID})
		cancel()
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if err == nil || errors.As(err, &started) {
			if err := store.SetWriteOutboxDelivered(ctx, job); err != nil && !errors.Is(err, delivery.ErrLeaseLost) {
				return err
			}
		} else {
			if err := store.ReleaseWriteOutbox(ctx, job); err != nil {
				return err
			}
		}
	}
	return nil
}

func RunWrites(ctx context.Context, service *delivery.WriteService, address, namespace, queue, owner string, ready func()) error {
	dial, cancel := context.WithTimeout(ctx, 10*time.Second)
	c, err := client.DialContext(dial, client.Options{HostPort: address, Namespace: namespace})
	cancel()
	if err != nil {
		return errors.New("temporal connection unavailable")
	}
	defer c.Close()
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = c.WorkflowService().DescribeNamespace(check, &workflowservice.DescribeNamespaceRequest{Namespace: namespace})
	cancel()
	if err != nil {
		return errors.New("temporal namespace unavailable")
	}
	w := worker.New(c, queue, worker.Options{MaxConcurrentActivityExecutionSize: 4, WorkerStopTimeout: 5 * time.Second})
	w.RegisterWorkflowWithOptions(WriteWorkflow, workflow.RegisterOptions{Name: WriteWorkflowName})
	a := writeActivities{service: service}
	w.RegisterActivityWithOptions(a.Begin, activity.RegisterOptions{Name: "write.begin"})
	w.RegisterActivityWithOptions(a.Step, activity.RegisterOptions{Name: "write.step"})
	w.RegisterActivityWithOptions(a.Block, activity.RegisterOptions{Name: "write.block"})
	w.RegisterActivityWithOptions(a.Finish, activity.RegisterOptions{Name: "write.finish"})
	if err := w.Start(); err != nil {
		return errors.New("temporal worker start failed")
	}
	defer w.Stop()
	if ready != nil {
		ready()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = DispatchWritesOnce(call, service.Store, c, queue, owner)
		cancel()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
