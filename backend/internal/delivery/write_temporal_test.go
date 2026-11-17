package delivery_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/orchestration"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/strictjson"
	"github.com/jackc/pgx/v5"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

func TestWriteTemporalPersistsQueueAndNeverReplaysUnknownEffect(t *testing.T) {
	address := os.Getenv("AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("set AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS for real Temporal write tests")
	}
	for _, mode := range []string{"success", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteFixture(t, func(b *delivery.Bundle) {
				for n := range b.Plan.Actions {
					a := &b.Plan.Actions[n]
					a.Proposals[0].Value = &planning.Value{Type: "string", Text: "business-value-not-in-temporal"}
					a.Hash = ""
					a.Hash = planning.Hash(*a)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			f.policy.writer.lost = mode == "lost-response"
			// A queued grant/ledger survives the API store closing before any worker exists.
			f.service.Store.Close()
			store, err := delivery.Open(ctx, f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			f.service.Store = store
			queue := "write-test-" + rand.Text()
			stop := startWriteWorker(t, f, address, queue)
			c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: "agent-platform-d4"})
			if err != nil {
				t.Fatal("temporal connection")
			}
			defer c.Close()
			deadline := time.Now().Add(30 * time.Second)
			var state string
			for time.Now().Before(deadline) {
				r, err := store.GetWriteExecution(ctx, f.ref(0))
				if err != nil {
					t.Fatal(err)
				}
				if r.State == "SUCCEEDED" || r.State == "UNKNOWN" {
					state = r.State
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			want := "SUCCEEDED"
			calls := 2
			if mode == "lost-response" {
				want = "UNKNOWN"
				calls = 1
			}
			if state != want {
				t.Fatalf("workflow state: %s", state)
			}
			var outcome string
			if err := c.GetWorkflow(ctx, f.run.WorkflowID, "").Get(ctx, &outcome); err != nil || outcome != want {
				t.Fatalf("workflow: %s %v", outcome, err)
			}
			stop()
			// Simulate dispatch ACK loss. A new worker must attach to the original
			// completed workflow, never create another attempt/execution history.
			conn, err := pgx.Connect(ctx, f.dsn)
			if err != nil {
				t.Fatal("database")
			}
			if _, err := conn.Exec(ctx, `UPDATE agent_delivery.write_outbox SET delivered_at=NULL,lease_until=NULL WHERE tenant_id=$1 AND run_id=$2`, f.run.TenantID, f.run.ID); err != nil {
				t.Fatal("reset dispatch acknowledgement")
			}
			stopAgain := startWriteWorker(t, f, address, queue)
			ack := false
			deadline = time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if conn.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM agent_delivery.write_outbox WHERE tenant_id=$1 AND run_id=$2`, f.run.TenantID, f.run.ID).Scan(&ack) != nil {
					t.Fatal("read outbox")
				}
				if ack {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			_ = conn.Close(ctx)
			stopAgain()
			if !ack || f.policy.writer.calls != calls {
				t.Fatal("restart redelivered a write")
			}
			assertWriteHistory(t, ctx, c, f.run)
			if mode == "lost-response" {
				result, err := f.service.Reconcile(ctx, f.ref(0))
				if err != nil || result != "BLOCKED" || f.policy.writer.calls != 1 {
					t.Fatal("unknown recovery started remainder")
				}
			}
		})
	}
}

func startWriteWorker(t *testing.T, f *writeFixture, address, queue string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- orchestration.RunWrites(ctx, f.service, address, "agent-platform-d4", queue, "writer-"+rand.Text(), func() { close(ready) })
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Error("worker stop timed out")
			}
		})
	}
	t.Cleanup(stop)
	select {
	case <-ready:
	case err := <-done:
		cancel()
		once.Do(func() {})
		t.Fatalf("worker startup: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("worker startup timed out")
	}
	return stop
}
func assertWriteHistory(t *testing.T, ctx context.Context, c client.Client, r delivery.WriteExecution) {
	t.Helper()
	history := c.GetWorkflowHistory(ctx, r.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	events := 0
	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			t.Fatal("read history")
		}
		events++
		inputs := []*commonpb.Payload{}
		if a := event.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			inputs = append(inputs, a.GetInput().GetPayloads()...)
		}
		if a := event.GetActivityTaskScheduledEventAttributes(); a != nil {
			inputs = append(inputs, a.GetInput().GetPayloads()...)
		}
		for _, payload := range inputs {
			var ref delivery.Ref
			if strictjson.Decode(payload.GetData(), &ref) != nil || ref.TenantID != r.TenantID || ref.RunID != r.ID {
				t.Fatal("non-reference workflow input")
			}
		}
		payloads := inputs
		if a := event.GetActivityTaskCompletedEventAttributes(); a != nil {
			payloads = append(payloads, a.GetResult().GetPayloads()...)
		}
		for _, payload := range payloads {
			for _, forbidden := range []string{"business-value-not-in-temporal", "setting-1", "intentHash", "reviewed actual writes"} {
				if strings.Contains(string(payload.GetData()), forbidden) {
					t.Fatal("business data entered Temporal history")
				}
			}
		}
	}
	if events == 0 {
		t.Fatal("missing history")
	}
}
