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
	tsversion "github.com/spacefleet/spacefleet/ent/tofustateversion"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// stateGuardFixture is an application whose workflow holds OpenTofu
// components, with helpers to seed their managed state and recorded applies.
type stateGuardFixture struct {
	t      *testing.T
	client *ent.Client
	svc    *Service
	orgID  uuid.UUID
	appID  uuid.UUID
}

func newStateGuardFixture(t *testing.T) *stateGuardFixture {
	t.Helper()
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	svc.SetManagedState(true)
	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	return &stateGuardFixture{t: t, client: client, svc: svc, orgID: org.ID, appID: app.ID}
}

// named is tofuComponent with a name.
func named(id uuid.UUID, name, backend string) ComponentInput {
	c := tofuComponent(id, backend)
	c.Name = name
	return c
}

func (f *stateGuardFixture) save(opts ReplaceOptions, comps ...ComponentInput) error {
	f.t.Helper()
	_, err := f.svc.ReplaceWorkflowWith(context.Background(), f.orgID, f.appID, stagesOf(comps), opts)
	return err
}

// writeState seeds a managed state row for the component with n versions
// (0 = a row with nothing written, as a first lock leaves it), optionally
// locked.
func (f *stateGuardFixture) writeState(componentID uuid.UUID, versions int, locked bool) {
	f.t.Helper()
	ctx := context.Background()
	create := f.client.TofuState.Create().
		SetOrganizationID(f.orgID).SetApplicationID(f.appID).SetComponentID(componentID).
		SetCurrentVersion(versions)
	if locked {
		create.SetLockID("lock-1").SetLockedAt(time.Now())
	}
	st, err := create.Save(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	for v := 1; v <= versions; v++ {
		if err := f.client.TofuStateVersion.Create().
			SetStateID(st.ID).SetOrganizationID(f.orgID).SetVersion(v).
			SetSerial(int64(v)).SetLineage("lin").SetSealed([]byte("sealed")).
			SetSizeBytes(6).SetMd5("x").Exec(ctx); err != nil {
			f.t.Fatal(err)
		}
	}
}

// recordApply records a succeeded apply of the component with the given
// inventory.
func (f *stateGuardFixture) recordApply(componentID uuid.UUID, resources string) {
	f.t.Helper()
	ctx := context.Background()
	run, err := f.client.WorkflowRun.Create().
		SetOrganizationID(f.orgID).SetApplicationID(f.appID).SetAction("deploy").
		SetStatus(workflowrun.StatusSucceeded).Save(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.client.ComponentRun.Create().
		SetOrganizationID(f.orgID).SetWorkflowRunID(run.ID).SetComponentID(DeriveApplyID(componentID)).
		SetStatus(componentrun.StatusSucceeded).SetOutputs("{}").SetResources(resources).
		SetFinishedAt(time.Now()).Save(ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *stateGuardFixture) stateRows(componentID uuid.UUID) int {
	f.t.Helper()
	n, err := f.client.TofuState.Query().Where(tstate.ComponentID(componentID)).Count(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

const oneResource = `[{"address":"null_resource.a","mode":"managed","type":"null_resource","name":"a","provider":"p","id":"1"}]`

// TestReplaceWorkflowDeletesManagedState: removing a managed-backend
// component that still manages resources is refused (StateDeletionError
// naming it, state kept) unless confirmed; confirmed, its state rows and
// every version go in the same save, while a kept component's state stays.
func TestReplaceWorkflowDeletesManagedState(t *testing.T) {
	f := newStateGuardFixture(t)
	ctx := context.Background()
	infra, dns := uuid.New(), uuid.New()
	if err := f.save(ReplaceOptions{}, named(infra, "infra", tofu.BackendSpacefleet), named(dns, "dns", tofu.BackendSpacefleet)); err != nil {
		t.Fatal(err)
	}
	f.writeState(infra, 2, false)
	f.recordApply(infra, oneResource)
	f.writeState(dns, 1, false)

	err := f.save(ReplaceOptions{}, named(dns, "dns", tofu.BackendSpacefleet))
	var refused *StateDeletionError
	if !errors.As(err, &refused) || !errors.Is(err, ErrStateDeletion) || len(refused.Components) != 1 || refused.Components[0] != "infra" {
		t.Fatalf("removing infra: err = %v, want a StateDeletionError naming infra", err)
	}
	if f.stateRows(infra) != 1 {
		t.Fatal("a refused save must keep the state")
	}

	if err := f.save(ReplaceOptions{AllowStateDeletion: true}, named(dns, "dns", tofu.BackendSpacefleet)); err != nil {
		t.Fatalf("confirmed removal: %v", err)
	}
	if f.stateRows(infra) != 0 {
		t.Error("infra's state should be deleted with it")
	}
	if n, err := f.client.TofuStateVersion.Query().Where(tsversion.OrganizationID(f.orgID)).Count(ctx); err != nil || n != 1 {
		t.Errorf("versions left = %d (err %v), want only dns's one", n, err)
	}
	if f.stateRows(dns) != 1 {
		t.Error("a kept component's state must stay")
	}
}

// TestReplaceWorkflowStateDeletionWithoutResources: state that tracks
// nothing goes without asking — a destroyed component (empty inventory), and
// a row with nothing written yet. State written without any recorded apply
// (an apply that failed after creating things) counts as managing
// resources.
func TestReplaceWorkflowStateDeletionWithoutResources(t *testing.T) {
	f := newStateGuardFixture(t)
	destroyed, fresh, failed := uuid.New(), uuid.New(), uuid.New()
	all := []ComponentInput{
		named(destroyed, "destroyed", tofu.BackendSpacefleet),
		named(fresh, "fresh", tofu.BackendSpacefleet),
		named(failed, "failed", tofu.BackendSpacefleet),
	}
	if err := f.save(ReplaceOptions{}, all...); err != nil {
		t.Fatal(err)
	}
	f.writeState(destroyed, 3, false)
	f.recordApply(destroyed, oneResource)
	f.recordApply(destroyed, `[]`)
	f.writeState(fresh, 0, false)
	f.writeState(failed, 1, false)

	if err := f.save(ReplaceOptions{}, all[2]); err != nil {
		t.Fatalf("removing destroyed + fresh: %v", err)
	}
	if f.stateRows(destroyed) != 0 || f.stateRows(fresh) != 0 {
		t.Error("their state should be deleted")
	}
	var refused *StateDeletionError
	if err := f.save(ReplaceOptions{}); !errors.As(err, &refused) || refused.Components[0] != "failed" {
		t.Fatalf("removing failed: err = %v, want a StateDeletionError naming it", err)
	}
}

// TestReplaceWorkflowStateDeletionRefusals: a locked state refuses the save
// whatever the flag, and so does a run in flight for the application.
func TestReplaceWorkflowStateDeletionRefusals(t *testing.T) {
	f := newStateGuardFixture(t)
	ctx := context.Background()
	locked, idle := uuid.New(), uuid.New()
	if err := f.save(ReplaceOptions{}, named(locked, "locked", tofu.BackendSpacefleet), named(idle, "idle", tofu.BackendSpacefleet)); err != nil {
		t.Fatal(err)
	}
	f.writeState(locked, 1, true)
	f.writeState(idle, 0, false)

	var lockErr *StateLockedError
	if err := f.save(ReplaceOptions{AllowStateDeletion: true}, named(idle, "idle", tofu.BackendSpacefleet)); !errors.As(err, &lockErr) || lockErr.Component != "locked" {
		t.Fatalf("removing a locked state: err = %v, want a StateLockedError naming it", err)
	}
	if f.stateRows(locked) != 1 {
		t.Fatal("a refused save must keep the state")
	}

	if _, err := f.client.WorkflowRun.Create().
		SetOrganizationID(f.orgID).SetApplicationID(f.appID).SetAction("deploy").
		SetStatus(workflowrun.StatusAwaitingApproval).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.save(ReplaceOptions{AllowStateDeletion: true}, named(locked, "locked", tofu.BackendSpacefleet)); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("removing state with a run in flight: err = %v, want ErrRunInFlight", err)
	}
	// Edits that remove no state are unaffected by the run.
	if err := f.save(ReplaceOptions{}, named(locked, "locked", tofu.BackendSpacefleet), named(idle, "renamed", tofu.BackendSpacefleet)); err != nil {
		t.Fatalf("an edit during a run: %v", err)
	}
}

// TestReplaceWorkflowCloudStateUntouched: removing a cloud-backend component
// needs no confirmation — its state is in the user's bucket — and a managed
// row it left behind before switching away is stale and deleted.
func TestReplaceWorkflowCloudStateUntouched(t *testing.T) {
	f := newStateGuardFixture(t)
	id := uuid.New()
	if err := f.save(ReplaceOptions{}, named(id, "infra", tofu.BackendS3)); err != nil {
		t.Fatal(err)
	}
	f.recordApply(id, oneResource)
	f.writeState(id, 1, false)
	if err := f.save(ReplaceOptions{}); err != nil {
		t.Fatalf("removing a cloud-backend component: %v", err)
	}
	if f.stateRows(id) != 0 {
		t.Error("the stale managed row should be deleted")
	}
}
