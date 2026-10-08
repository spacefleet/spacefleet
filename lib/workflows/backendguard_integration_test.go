//go:build integration

package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// tofuComponent is a valid OpenTofu component input on the given backend.
func tofuComponent(id uuid.UUID, backend string) ComponentInput {
	cfg := map[string]string{
		helm.ConfigRepoURL:     "https://github.com/acme/infra",
		manifestConfigPath:     "envs/prod",
		terraformConfigBackend: backend,
	}
	if backend == tofu.BackendS3 {
		cfg[terraformConfigBackendConfig] = `{"bucket":"b","key":"k","region":"us-east-1"}`
	}
	return ComponentInput{ID: id, Name: "infra", Type: TypeTerraform, Config: cfg}
}

// TestReplaceWorkflowManagedStateNeedsKey: without a secret key the save
// refuses the spacefleet backend with an actionable message; with one it
// saves.
func TestReplaceWorkflowManagedStateNeedsKey(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")

	stages := stagesOf([]ComponentInput{tofuComponent(uuid.New(), tofu.BackendSpacefleet)})
	if _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, stages); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("without a key: err = %v, want ErrInvalidConfig", err)
	}
	svc.SetManagedState(true)
	if _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, stages); err != nil {
		t.Fatalf("with a key: %v", err)
	}
}

// TestReplaceWorkflowGuardsBackendChanges: switching the backend of an
// OpenTofu component whose recorded state still lists resources is refused
// (ErrBackendChange naming it) unless confirmed; a component that never
// applied, or whose last recorded state is empty, switches freely; another
// application's history doesn't count.
func TestReplaceWorkflowGuardsBackendChanges(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	svc.SetManagedState(true)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	id := uuid.New()

	save := func(backend string, opts ReplaceOptions) error {
		_, err := svc.ReplaceWorkflowWith(ctx, org.ID, app.ID, stagesOf([]ComponentInput{tofuComponent(id, backend)}), opts)
		return err
	}
	if err := save(tofu.BackendS3, ReplaceOptions{}); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	// Never applied: switching is free.
	if err := save(tofu.BackendSpacefleet, ReplaceOptions{}); err != nil {
		t.Fatalf("switch before any apply: %v", err)
	}

	// An apply recorded resources.
	recordApply := func(appID uuid.UUID, resources string, finished time.Time) {
		t.Helper()
		run, err := client.WorkflowRun.Create().SetOrganizationID(org.ID).SetApplicationID(appID).SetAction("deploy").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ComponentRun.Create().
			SetOrganizationID(org.ID).SetWorkflowRunID(run.ID).SetComponentID(DeriveApplyID(id)).
			SetStatus(componentrun.StatusSucceeded).SetOutputs("{}").SetResources(resources).
			SetFinishedAt(finished).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	recordApply(app.ID, `[{"address":"null_resource.a","mode":"managed","type":"null_resource","name":"a","provider":"p","id":"1"}]`, now.Add(-time.Hour))

	err := save(tofu.BackendS3, ReplaceOptions{})
	var change *BackendChangeError
	if !errors.As(err, &change) || !errors.Is(err, ErrBackendChange) || len(change.Components) != 1 || change.Components[0] != "infra" {
		t.Fatalf("switch with resources: err = %v, want a BackendChangeError naming infra", err)
	}
	// Re-saving on the same backend is unaffected.
	if err := save(tofu.BackendSpacefleet, ReplaceOptions{}); err != nil {
		t.Fatalf("same backend: %v", err)
	}
	// Another application's apply of the same id is not this one's history.
	other := newApp(t, client, org.ID, "other")
	recordApply(other.ID, `[]`, now)
	if err := save(tofu.BackendS3, ReplaceOptions{}); !errors.Is(err, ErrBackendChange) {
		t.Fatalf("another app's empty state must not clear the guard: err = %v", err)
	}
	// Confirmed: the switch goes through.
	if err := save(tofu.BackendS3, ReplaceOptions{AllowBackendChange: true}); err != nil {
		t.Fatalf("confirmed switch: %v", err)
	}
	// After a destroy (empty inventory), switching is free again.
	recordApply(app.ID, `[]`, now.Add(time.Minute))
	if err := save(tofu.BackendSpacefleet, ReplaceOptions{}); err != nil {
		t.Fatalf("switch after destroy: %v", err)
	}
}
