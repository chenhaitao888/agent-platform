package reference_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-platform/backend/internal/artifact"
	"agent-platform/backend/internal/change"
	"agent-platform/backend/internal/connector/reference"
	"agent-platform/backend/internal/delivery"
	"agent-platform/backend/internal/planning"
	"agent-platform/backend/internal/resourcewrite"
	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/task"
	"github.com/jackc/pgx/v5"
)

type referenceWritePolicy struct {
	planner            *planning.Planner
	config, experiment resourcewrite.Writer
}

func (p referenceWritePolicy) Authorize(_ context.Context, subject string, _ delivery.Bundle, _ string) (string, error) {
	if subject != "alice" && subject != "bob" {
		return "", delivery.ErrPolicy
	}
	return "fixture-policy-" + subject, nil
}
func (p referenceWritePolicy) Writer(_ context.Context, kind string, target planning.Target) (resourcewrite.Writer, error) {
	if kind == "config" && target == configTarget {
		return p.config, nil
	}
	if kind == "experiment" && target == experimentTarget {
		return p.experiment, nil
	}
	return nil, delivery.ErrPolicy
}
func (p referenceWritePolicy) Revalidate(ctx context.Context, b delivery.Bundle, prefix []resourcewrite.Completed) error {
	err := resourcewrite.RevalidateRemaining(ctx, p.planner, b.Plan, prefix)
	if errors.Is(err, planning.ErrDrift) {
		return delivery.ErrDrift
	}
	return err
}
func referencePolicy(t *testing.T, p *writePlatform) referenceWritePolicy {
	t.Helper()
	connection := reference.Connection{ID: configTarget.ConnectorRef, Version: "1", BaseURL: p.server.URL, Token: "writer-fixture-token"}
	cr, err := reference.NewConfigReader(connection, p.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	connection.ID = experimentTarget.ConnectorRef
	er, err := reference.NewExperimentReader(connection, p.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	planner, err := planning.NewPlanner(map[string]planning.ConfigReader{configTarget.ConnectorRef: cr}, map[string]planning.ExperimentReader{experimentTarget.ConnectorRef: er}, planning.DiffAnalyzer{})
	if err != nil {
		t.Fatal(err)
	}
	return referenceWritePolicy{planner: planner, config: writerFor(t, p, "config"), experiment: writerFor(t, p, "experiment")}
}

func TestDeliveryControlPlaneUsesReferenceHTTPSWritersAndDurableReceipts(t *testing.T) {
	for _, mode := range []string{"success", "response-loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backend := newPostgresWriteBackend(t, initialWriteDatabase())
			platform := newWritePlatform(t, backend)
			store, err := delivery.Open(ctx, backend.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			b := referenceDeliveryBundle()
			r := delivery.Request{TenantID: b.TenantID, ChangeID: b.ChangeID, PlanID: b.Plan.ID, PlanHash: b.Plan.Hash, IdempotencyKey: rand.Text()}
			b, _, err = store.Save(ctx, r, b)
			if err != nil {
				t.Fatal(err)
			}
			service := &delivery.WriteService{Store: store, Policy: referencePolicy(t, platform)}
			a, _, err := service.Approve(ctx, delivery.ApprovalInput{TenantID: b.TenantID, BundleID: b.ID, ExpectedDigest: b.Digest, Subject: "bob", IdempotencyKey: rand.Text(), TTLSeconds: 3600, Reason: "reviewed reference config and AB draft", FindingsReviewed: true})
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := service.Start(ctx, delivery.WriteStartInput{TenantID: b.TenantID, BundleID: b.ID, ExpectedDigest: b.Digest, ApprovalID: a.ID, Subject: "alice", IdempotencyKey: rand.Text()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				conn, err := pgx.Connect(ctx, backend.dsn)
				if err != nil {
					t.Error("outbox cleanup connection")
					return
				}
				defer func() { _ = conn.Close(ctx) }()
				if _, err := conn.Exec(ctx, `DELETE FROM agent_delivery.write_outbox WHERE tenant_id=$1 AND run_id=$2`, run.TenantID, run.ID); err != nil {
					t.Error("outbox cleanup")
				}
			})
			ref := delivery.Ref{TenantID: run.TenantID, RunID: run.ID}
			if mode == "response-loss" {
				platform.drop.Store(true)
			}
			for _, op := range run.Operations {
				ref.OperationID = op.ID
				next, err := service.Step(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "response-loss" {
					if next {
						t.Fatal("lost response reported success")
					}
					break
				}
				if !next {
					t.Fatal("reference writer failed")
				}
			}
			want := "SUCCEEDED"
			effects := 3
			if mode == "response-loss" {
				store.Close()
				backend.reopen(t)
				platform.server.Close()
				platform = newWritePlatform(t, backend)
				service.Policy = referencePolicy(t, platform)
				reopened, err := delivery.Open(ctx, backend.dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				service.Store = reopened
				state, err := service.Reconcile(ctx, ref)
				if err != nil || state != "BLOCKED" {
					t.Fatalf("durable lookup: %s %v", state, err)
				}
				want = "BLOCKED"
				effects = 1
			}
			state, err := service.Finish(ctx, ref)
			if err != nil || state != want {
				t.Fatalf("finish: %s %v", state, err)
			}
			data := snapshotWriteBackend(t, backend)
			if data.Effects != effects || len(data.Receipts) != effects || data.Config.Items[0].Value.Text != "20" {
				t.Fatal("wrong durable effects")
			}
			if mode == "success" && (len(data.Experiments.Items) != 1 || data.Experiments.Items[0].State != "DRAFT") {
				t.Fatal("AB draft lifecycle")
			}
			got, err := service.Store.GetWriteExecution(ctx, ref)
			if err != nil || got.Operations[0].State != "VERIFIED" {
				t.Fatal("committed evidence missing")
			}
		})
	}
}

func referenceDeliveryBundle() delivery.Bundle {
	p := writePlan()
	p.ID = "plan-" + rand.Text()
	p.CreatedBy = "alice"
	p.CreatedAt = time.Now().UTC()
	p.SnapshotID = "snapshot-1"
	p.SnapshotHash = strings.Repeat("d", 64)
	p.Issues = []planning.Issue{}
	repo := task.RepositoryReference{Provider: "gitlab", RepositoryID: "1", TargetBranch: "main", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("c", 40)}
	source := planning.Source{ServiceID: "S1", HeadSHA: repo.HeadSHA, BaseSHA: repo.BaseSHA, Review: planning.ReviewEvidence{TaskID: "task-1", ExecutionID: "review-1", ArtifactID: "artifact-1", State: "SUCCEEDED"}, ConfigCoverage: "CANDIDATES_FOUND", ExperimentCoverage: "CANDIDATES_FOUND", Candidates: []planning.Candidate{}}
	p.Services = []planning.Source{source}
	p.Observations = []planning.Observation{}
	for _, n := range []int{0, 2} {
		a := p.Actions[n]
		p.Observations = append(p.Observations, planning.Observation{Kind: a.Kind, Target: a.Target, Descriptor: a.Descriptor, Version: a.ResourceVersion, ObservedAt: time.Now().UTC()})
	}
	p.Hash = ""
	p.Hash = planning.Hash(p)
	content, _ := json.Marshal(review.FindingsReport{SchemaVersion: "1.0", BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA, Findings: []review.Finding{}})
	meta := artifact.Artifact{ID: "artifact-1", TenantID: p.TenantID, TaskID: "task-1", WorkspaceID: "workspace-1", Type: artifact.TypePRReviewFindings, MediaType: "application/json", SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC()}
	return delivery.Bundle{TenantID: p.TenantID, ChangeID: "change-" + rand.Text(), Title: "config and AB draft", Environment: p.Environment, CreatedBy: "alice", Plan: p, Snapshot: change.Snapshot{ID: p.SnapshotID, State: "READY", InputHash: p.SnapshotHash, Services: []change.ServiceSnapshot{{ServiceID: "S1", CatalogVersion: "1", ConnectorRef: "gitlab", SourceBranch: "feature", TargetBranch: "main", Repository: &repo}}}, Evidence: []delivery.Evidence{{ServiceID: "S1", Content: content, Execution: review.Execution{ID: "review-1", TenantID: p.TenantID, TaskID: "task-1", WorkspaceID: "workspace-1", WorkspaceVersion: 1, BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA, State: review.ExecutionSucceeded, Artifact: &meta}}}}
}
