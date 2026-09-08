//go:build integration

package workflows

import (
	"context"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/planpolicy"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

const planWithDBDelete = "OpenTofu will perform the following actions:\n\n  # aws_db_instance.main will be destroyed\n  - resource \"aws_db_instance\" \"main\" {\n    }\n\n  # aws_instance.web will be created\n  + resource \"aws_instance\" \"web\" {\n    }\n\nPlan: 1 to add, 0 to change, 1 to destroy.\n"

const denyDBDeletes = "package spacefleet\n\ndeny contains msg if {\n\tsome r in input.plan.resources\n\tr.action == \"delete\"\n\tstartswith(r.type, \"aws_db\")\n\tmsg := sprintf(\"%s would be destroyed\", [r.address])\n}\n"

// TestWorkPolicyGate drives the policy gate through the worker: a block
// policy that the plan violates fails the plan step (with the verdict
// recorded), skips the apply, and fails the run; the same plan on a preview
// records the verdict and succeeds; a warn policy records and proceeds; a
// disabled policy and one limited to another application take no part.
func TestWorkPolicyGate(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	other := newApp(t, client, org.ID, "other")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	mkPolicy := func(name, enforcement string, enabled bool, appID *ent.Application) *ent.PlanPolicy {
		c := client.PlanPolicy.Create().SetOrganizationID(org.ID).SetName(name).SetRego(denyDBDeletes).
			SetEnforcement(planpolicy.Enforcement(enforcement)).SetEnabled(enabled)
		if appID != nil {
			c.SetApplicationID(appID.ID)
		}
		p, err := c.Save(ctx)
		if err != nil {
			t.Fatalf("create policy %s: %v", name, err)
		}
		return p
	}
	blocker := mkPolicy("no-db-deletes", "block", true, nil)
	mkPolicy("disabled", "block", false, nil)
	mkPolicy("elsewhere", "block", true, other)

	w := newTestWorker(client, succeedingFuncs())
	w.captureLogs = func(context.Context, k8s.Connection, string) string { return planWithDBDelete }

	run, args := beginRun(t, svc, org.ID, app.ID)
	// A policy block is a deterministic failure: the run settles without a
	// retry, so Work returns nil (see the worker's F4 posture).
	if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusFailed {
		t.Fatalf("run = %q, want failed", got)
	}
	planCR := componentRunFor(t, client, run.ID, tf.ID)
	if planCR.Status != componentrun.StatusFailed || !strings.Contains(planCR.Message, "blocked by policy: no-db-deletes: aws_db_instance.main would be destroyed") {
		t.Errorf("plan step = %s %q", planCR.Status, planCR.Message)
	}
	v, ok := PolicyVerdictOf(planCR)
	if !ok || !v.Blocked || len(v.Results) != 1 || v.Results[0].PolicyID != blocker.ID.String() || len(v.Results[0].Violations) != 1 {
		t.Errorf("verdict = %+v (ok=%v), want one blocking result", v, ok)
	}
	if applyCR := componentRunFor(t, client, run.ID, deriveApplyID(tf.ID)); applyCR.Status != componentrun.StatusSkipped {
		t.Errorf("apply step = %s, want skipped", applyCR.Status)
	}

	// A preview records the verdict but never fails on it.
	preview, err := svc.BeginRun(ctx, org.ID, app.ID, ActionPreview)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Work(ctx, workerJob(WorkflowRunArgs{WorkflowRunID: preview.ID, OrgID: org.ID, ApplicationID: app.ID, Action: ActionPreview}, 1, 3)); err != nil {
		t.Fatalf("preview Work: %v", err)
	}
	if got := runStatus(t, client, preview.ID); got != workflowrun.StatusSucceeded {
		t.Errorf("preview run = %q, want succeeded", got)
	}
	if v, ok := PolicyVerdictOf(componentRunFor(t, client, preview.ID, tf.ID)); !ok || !v.Blocked {
		t.Errorf("preview verdict = %+v (ok=%v), want recorded and blocked", v, ok)
	}

	// Downgraded to warn: the deploy proceeds with the verdict recorded.
	if _, err := client.PlanPolicy.UpdateOneID(blocker.ID).SetEnforcement(planpolicy.EnforcementWarn).Save(ctx); err != nil {
		t.Fatal(err)
	}
	run2, args2 := beginRun(t, svc, org.ID, app.ID)
	if err := w.Work(ctx, workerJob(args2, 1, 3)); err != nil {
		t.Fatalf("warn Work: %v", err)
	}
	if got := runStatus(t, client, run2.ID); got != workflowrun.StatusSucceeded {
		t.Errorf("warn run = %q, want succeeded", got)
	}
	if v, ok := PolicyVerdictOf(componentRunFor(t, client, run2.ID, tf.ID)); !ok || v.Blocked || !v.Warned {
		t.Errorf("warn verdict = %+v (ok=%v)", v, ok)
	}

	// No applicable policies: no verdict at all.
	if _, err := client.PlanPolicy.UpdateOneID(blocker.ID).SetEnabled(false).Save(ctx); err != nil {
		t.Fatal(err)
	}
	run3, args3 := beginRun(t, svc, org.ID, app.ID)
	if err := w.Work(ctx, workerJob(args3, 1, 3)); err != nil {
		t.Fatalf("no-policy Work: %v", err)
	}
	if cr := componentRunFor(t, client, run3.ID, tf.ID); cr.Policy != "" {
		t.Errorf("verdict recorded with no applicable policy: %s", cr.Policy)
	}
}
