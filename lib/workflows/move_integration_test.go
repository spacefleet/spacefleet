//go:build integration

package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	tstate "github.com/spacefleet/spacefleet/ent/tofustate"
	"github.com/spacefleet/spacefleet/ent/variable"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// moveFixture is two applications of one org: web, whose workflow holds an
// OpenTofu component on managed state with a variable, a state version and a
// recorded apply; and api, with a workflow of its own.
type moveFixture struct {
	t        *testing.T
	client   *ent.Client
	svc      *Service
	orgID    uuid.UUID
	web, api *ent.Application
	infra    uuid.UUID
	apiStage uuid.UUID
	webSaved []StageInput
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	ctx := context.Background()
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	svc.SetManagedState(true)
	org := newOrg(t, client, "Acme")
	f := &moveFixture{t: t, client: client, svc: svc, orgID: org.ID,
		web: newApp(t, client, org.ID, "web"), api: newApp(t, client, org.ID, "api"), infra: uuid.New()}
	f.webSaved = stagesOf([]ComponentInput{named(f.infra, "infra", tofu.BackendSpacefleet)})
	if _, err := svc.ReplaceWorkflow(ctx, org.ID, f.web.ID, f.webSaved); err != nil {
		t.Fatal(err)
	}
	apiStages := stagesOf([]ComponentInput{named(uuid.New(), "db", tofu.BackendSpacefleet)})
	f.apiStage = apiStages[0].ID
	if _, err := svc.ReplaceWorkflow(ctx, org.ID, f.api.ID, apiStages); err != nil {
		t.Fatal(err)
	}
	if err := client.Variable.Create().SetOrganizationID(org.ID).SetApplicationID(f.web.ID).
		SetComponentID(f.infra).SetName("REGION").SetValue("eu").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.TofuState.Create().SetOrganizationID(org.ID).SetApplicationID(f.web.ID).
		SetComponentID(f.infra).SetCurrentVersion(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := client.WorkflowRun.Create().SetOrganizationID(org.ID).SetApplicationID(f.web.ID).
		SetAction("deploy").SetStatus(workflowrun.StatusSucceeded).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ComponentRun.Create().SetOrganizationID(org.ID).SetWorkflowRunID(run.ID).
		SetComponentID(DeriveApplyID(f.infra)).SetStatus(componentrun.StatusSucceeded).
		SetOutputs("{}").SetResources(oneResource).SetFinishedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *moveFixture) move(in MoveInput) (*ent.Component, error) {
	return f.svc.MoveComponent(context.Background(), f.orgID, f.web.ID, f.infra, in)
}

// TestMoveComponent: moving to another application keeps the id and takes
// the component's variables and managed state along; its recorded state,
// and the guards that read it, follow it; its runs stay with the old
// application; and a builder still holding the old workflow can't save the
// component back.
func TestMoveComponent(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	moved, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.ID != f.infra || moved.ApplicationID != f.api.ID || moved.StageID != f.apiStage || moved.Ordinal != 1 || moved.Name != "infra" {
		t.Errorf("moved = %+v, want infra at the end of api's stage", moved)
	}
	if n, _ := f.client.Variable.Query().Where(variable.ApplicationID(f.api.ID), variable.ComponentID(f.infra)).Count(ctx); n != 1 {
		t.Errorf("variables under api = %d, want 1", n)
	}
	if n, _ := f.client.TofuState.Query().Where(tstate.ApplicationID(f.api.ID), tstate.ComponentID(f.infra)).Count(ctx); n != 1 {
		t.Errorf("state rows under api = %d, want 1", n)
	}
	// The history follows the component.
	if _, err := f.svc.LatestComponentState(ctx, f.orgID, f.api.ID, f.infra); err != nil {
		t.Errorf("LatestComponentState under api: %v", err)
	}
	if _, err := f.svc.LatestComponentState(ctx, f.orgID, f.web.ID, f.infra); !ent.IsNotFound(err) {
		t.Errorf("LatestComponentState under web: err = %v, want NotFound", err)
	}
	if manages, err := f.svc.managesResources(ctx, f.orgID, f.infra); err != nil || !manages {
		t.Errorf("managesResources = %v (err %v), want the recorded resource", manages, err)
	}
	// So api's backend guard still refuses a switch, and removing it there
	// still asks before deleting its state.
	apiNow, err := f.svc.GetWorkflow(ctx, f.orgID, f.api.ID)
	if err != nil {
		t.Fatal(err)
	}
	asInputs := func(backend string, drop bool) []StageInput {
		st := StageInput{ID: apiNow[0].ID, Name: apiNow[0].Name}
		for _, c := range apiNow[0].Edges.Components {
			if c.ID == f.infra {
				if drop {
					continue
				}
				st.Components = append(st.Components, named(c.ID, c.Name, backend))
				continue
			}
			st.Components = append(st.Components, named(c.ID, c.Name, tofu.BackendSpacefleet))
		}
		return []StageInput{st}
	}
	if _, err := f.svc.ReplaceWorkflow(ctx, f.orgID, f.api.ID, asInputs(tofu.BackendS3, false)); !errors.Is(err, ErrBackendChange) {
		t.Errorf("backend switch after the move: err = %v, want ErrBackendChange", err)
	}
	if _, err := f.svc.ReplaceWorkflow(ctx, f.orgID, f.api.ID, asInputs(tofu.BackendSpacefleet, true)); !errors.Is(err, ErrStateDeletion) {
		t.Errorf("removal after the move: err = %v, want ErrStateDeletion", err)
	}
	// Runs stay with web.
	if n, _ := f.client.WorkflowRun.Query().Where(workflowrun.ApplicationID(f.web.ID)).Count(ctx); n != 1 {
		t.Errorf("web runs = %d, want its history kept", n)
	}
	// A stale web draft still carrying infra is refused, naming where it went.
	var movedErr *ComponentMovedError
	if _, err := f.svc.ReplaceWorkflow(ctx, f.orgID, f.web.ID, f.webSaved); !errors.As(err, &movedErr) || movedErr.Application != "api" {
		t.Errorf("stale draft: err = %v, want a ComponentMovedError naming api", err)
	}
}

// TestMoveComponentRefusals: a name taken in the destination (until renamed
// on the way), another org's application, a stage of another application,
// a run in flight in either application, and a locked state.
func TestMoveComponentRefusals(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	other := newApp(t, f.client, newOrg(t, f.client, "Other").ID, "elsewhere")
	if _, err := f.move(MoveInput{ApplicationID: other.ID, NewStageName: "x"}); !errors.Is(err, ErrMoveTarget) {
		t.Errorf("another org: err = %v, want ErrMoveTarget", err)
	}
	webStage := f.webSaved[0].ID
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &webStage}); !errors.Is(err, ErrMoveTarget) {
		t.Errorf("another app's stage: err = %v, want ErrMoveTarget", err)
	}
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID}); !errors.Is(err, ErrMoveTarget) {
		t.Errorf("no stage: err = %v, want ErrMoveTarget", err)
	}
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage, Name: "db"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken: err = %v, want ErrNameTaken", err)
	}
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage, Name: "Not A Slug"}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad name: err = %v, want ErrInvalidConfig", err)
	}

	run, err := f.client.WorkflowRun.Create().SetOrganizationID(f.orgID).SetApplicationID(f.api.ID).
		SetAction("deploy").SetStatus(workflowrun.StatusRunning).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage}); !errors.Is(err, ErrRunInFlight) {
		t.Errorf("run in flight in the destination: err = %v, want ErrRunInFlight", err)
	}
	if err := f.svc.MarkRun(ctx, f.orgID, run.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := f.client.TofuState.Update().Where(tstate.ComponentID(f.infra)).SetLockID("held").Save(ctx); err != nil {
		t.Fatal(err)
	}
	var locked *StateLockedError
	if _, err := f.move(MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage}); !errors.As(err, &locked) {
		t.Errorf("locked state: err = %v, want StateLockedError", err)
	}
}

// TestMoveComponentNewStageAndRename: a new stage is added at the end of the
// destination's workflow, and the component can be renamed on the way —
// including within its own application.
func TestMoveComponentNewStageAndRename(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	moved, err := f.move(MoveInput{ApplicationID: f.api.ID, NewStageName: " Infra ", Name: "db"})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename onto a taken name: err = %v", err)
	}
	moved, err = f.move(MoveInput{ApplicationID: f.api.ID, NewStageName: " Infra ", Name: "network"})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	st, err := f.client.WorkflowStage.Query().Where(workflowstage.ID(moved.StageID)).Only(ctx)
	if err != nil || st.Name != "Infra" || st.Ordinal != 1 || st.ApplicationID != f.api.ID || moved.Name != "network" || moved.Ordinal != 0 {
		t.Errorf("stage = %+v (err %v), component = %+v; want a new second stage holding network", st, err, moved)
	}
	// Back within api: to its first stage.
	back, err := f.svc.MoveComponent(ctx, f.orgID, f.api.ID, f.infra, MoveInput{ApplicationID: f.api.ID, StageID: &f.apiStage})
	if err != nil || back.StageID != f.apiStage || back.Ordinal != 1 {
		t.Errorf("move within api = %+v (err %v)", back, err)
	}
}
