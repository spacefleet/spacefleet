//go:build integration

package workflows

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/variable"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// TestReplaceWorkflowReconcilesComponentVariables proves that replacing a
// workflow drops the variables of a component that was removed, while keeping
// those of a component that persists and any app-level variables.
func TestReplaceWorkflowReconcilesComponentVariables(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	target := newCluster(t, client, org.ID, "target").ID

	helmCfg := map[string]string{
		"chart_source": "http_repo",
		"repo_url":     "https://charts.example.com",
		"chart":        "web",
	}
	idA, idB := uuid.New(), uuid.New()

	// Start with components A and B.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID,
		[]ComponentInput{
			{ID: idA, Name: "a", Type: "helm", Config: helmCfg, TargetClusterID: &target, TargetNamespace: "prod"},
			{ID: idB, Name: "b", Type: "helm", Config: helmCfg, TargetClusterID: &target, TargetNamespace: "prod"},
		}, nil); err != nil {
		t.Fatalf("ReplaceWorkflow (A,B): %v", err)
	}

	// A variable on each component, plus one app-level variable.
	mkVar := func(name string, componentID *uuid.UUID) {
		create := client.Variable.Create().
			SetOrganizationID(org.ID).
			SetApplicationID(app.ID).
			SetName(name).
			SetValue("x")
		if componentID != nil {
			create = create.SetComponentID(*componentID)
		}
		if _, err := create.Save(ctx); err != nil {
			t.Fatalf("create variable %q: %v", name, err)
		}
	}
	mkVar("VA", &idA)
	mkVar("VB", &idB)
	mkVar("APP", nil)

	// Replace with only A — B is removed.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID,
		[]ComponentInput{{ID: idA, Name: "a", Type: "helm", Config: helmCfg, TargetClusterID: &target, TargetNamespace: "prod"}}, nil); err != nil {
		t.Fatalf("ReplaceWorkflow (A): %v", err)
	}

	// A's variable survives (its component persisted with the same id).
	if n, err := client.Variable.Query().Where(variable.ComponentID(idA)).Count(ctx); err != nil || n != 1 {
		t.Errorf("A's variables = %d (err %v), want 1", n, err)
	}
	// B's variable was reconciled away.
	if n, err := client.Variable.Query().Where(variable.ComponentID(idB)).Count(ctx); err != nil || n != 0 {
		t.Errorf("B's variables = %d (err %v), want 0 (component removed)", n, err)
	}
	// The app-level variable is untouched.
	if n, err := client.Variable.Query().Where(variable.ComponentIDIsNil()).Count(ctx); err != nil || n != 1 {
		t.Errorf("app-level variables = %d (err %v), want 1", n, err)
	}
}

// TestReplaceWorkflowEmptyDropsAllComponentVariables proves replacing with no
// components drops every component-level variable but keeps app-level ones.
func TestReplaceWorkflowEmptyDropsAllComponentVariables(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	target := newCluster(t, client, org.ID, "target").ID
	id := uuid.New()
	helmCfg := map[string]string{"chart_source": "http_repo", "repo_url": "https://x", "chart": "c"}

	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID,
		[]ComponentInput{{ID: id, Name: "a", Type: "helm", Config: helmCfg, TargetClusterID: &target, TargetNamespace: "prod"}}, nil); err != nil {
		t.Fatalf("ReplaceWorkflow: %v", err)
	}
	if _, err := client.Variable.Create().SetOrganizationID(org.ID).SetApplicationID(app.ID).SetComponentID(id).SetName("V").SetValue("x").Save(ctx); err != nil {
		t.Fatalf("create component variable: %v", err)
	}
	if _, err := client.Variable.Create().SetOrganizationID(org.ID).SetApplicationID(app.ID).SetName("APP").SetValue("x").Save(ctx); err != nil {
		t.Fatalf("create app variable: %v", err)
	}

	// Replace with an empty workflow.
	if _, _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, nil, nil); err != nil {
		t.Fatalf("ReplaceWorkflow (empty): %v", err)
	}

	if n, err := client.Variable.Query().Where(variable.ComponentIDNotNil()).Count(ctx); err != nil || n != 0 {
		t.Errorf("component variables = %d (err %v), want 0", n, err)
	}
	if n, err := client.Variable.Query().Where(variable.ComponentIDIsNil()).Count(ctx); err != nil || n != 1 {
		t.Errorf("app-level variables = %d (err %v), want 1", n, err)
	}
}
