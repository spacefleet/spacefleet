package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	tstate "github.com/spacefleet/spacefleet/ent/tofustate"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// ErrStateDeletion is returned by a workflow save that removes an OpenTofu
// component on the managed backend whose recorded state still lists
// resources. Removing a component deletes its managed state — nothing could
// reach it again — so the resources would keep running with no state left to
// manage them. A handler maps it to 409 so the caller can confirm
// (ReplaceOptions.AllowStateDeletion).
var ErrStateDeletion = errors.New("workflows: managed state deletion")

// StateDeletionError names the components whose state deletion was refused.
type StateDeletionError struct {
	// Components are the refused components, by name.
	Components []string
}

func (e *StateDeletionError) Error() string {
	return fmt.Sprintf("removing %s deletes its managed state, which still lists resources: they would keep running with nothing left to manage them (destroy it first, download the state, or confirm the deletion)",
		strings.Join(e.Components, ", "))
}

func (e *StateDeletionError) Unwrap() error { return ErrStateDeletion }

// ErrStateLocked is returned by a workflow save that would delete a managed
// state whose lock is held: a step may be writing it. Confirming doesn't
// override it. A handler maps it to 409.
var ErrStateLocked = errors.New("workflows: managed state is locked")

// StateLockedError names the component whose locked state refused the save.
type StateLockedError struct {
	Component string
}

func (e *StateLockedError) Error() string {
	return fmt.Sprintf("%s's managed state is locked, so removing it can't delete the state; wait for the run holding the lock to finish, or release the lock first", e.Component)
}

func (e *StateLockedError) Unwrap() error { return ErrStateLocked }

// stateDeletion is the managed state a workflow save deletes: the state rows
// of the components it removes.
type stateDeletion struct {
	components []uuid.UUID
	rows       int
}

// checkStateDeletions decides what a save does to the managed state of the
// OpenTofu components it removes (stored in the application, absent from
// nodes). Their state is deleted with them, in the save's transaction (see
// deleteStates) — no page or API could reach it again. Before that, the save
// is refused when:
//
//   - a removed component's state is locked (a step may be mid-write) —
//     whatever the caller confirmed;
//   - a run is in flight for the application (its snapshot may still read or
//     write that state, and would recreate it);
//   - a removed component on the managed backend still manages resources and
//     the caller didn't confirm (allow) — the resources would keep running
//     with no state.
//
// A cloud backend's state lives in the user's bucket and is never touched;
// rows a component left behind on the managed backend before switching away
// are stale and deleted without asking.
func (s *Service) checkStateDeletions(ctx context.Context, orgID, appID uuid.UUID, nodes []ComponentInput, allow bool) (stateDeletion, error) {
	keep := make(map[uuid.UUID]bool, len(nodes))
	for _, n := range nodes {
		keep[n.ID] = true
	}
	stored, err := s.ent.Component.Query().
		Where(
			component.OrganizationID(orgID),
			component.ApplicationID(appID),
			component.TypeEQ(component.TypeTerraform),
		).
		Order(ent.Asc(component.FieldOrdinal), ent.Asc(component.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return stateDeletion{}, err
	}
	var removed []*ent.Component
	for _, c := range stored {
		if !keep[c.ID] {
			removed = append(removed, c)
		}
	}
	if len(removed) == 0 {
		return stateDeletion{}, nil
	}
	ids := make([]uuid.UUID, len(removed))
	for i, c := range removed {
		ids[i] = c.ID
	}
	rows, err := s.ent.TofuState.Query().
		Where(
			tstate.OrganizationID(orgID),
			tstate.ApplicationID(appID),
			tstate.ComponentIDIn(ids...),
		).
		All(ctx)
	if err != nil {
		return stateDeletion{}, err
	}
	if len(rows) == 0 {
		return stateDeletion{}, nil
	}
	statesOf := make(map[uuid.UUID][]*ent.TofuState, len(rows))
	for _, r := range rows {
		statesOf[r.ComponentID] = append(statesOf[r.ComponentID], r)
	}

	var del stateDeletion
	for _, c := range removed {
		states := statesOf[c.ID]
		if len(states) == 0 {
			continue
		}
		for _, st := range states {
			if st.LockID != nil {
				return stateDeletion{}, &StateLockedError{Component: c.Name}
			}
		}
		del.components = append(del.components, c.ID)
		del.rows += len(states)
	}
	if err := s.assertNoRunInFlight(ctx, orgID, appID); err != nil {
		return stateDeletion{}, fmt.Errorf("%w: removing a component deletes its managed state, which the run in progress may still use; try again when it finishes", err)
	}
	if allow {
		return del, nil
	}
	var refused []string
	for _, c := range removed {
		if c.Config[terraformConfigBackend] != tofu.BackendSpacefleet || len(statesOf[c.ID]) == 0 {
			continue
		}
		manages, err := s.stateManagesResources(ctx, orgID, c.ID, statesOf[c.ID])
		if err != nil {
			return stateDeletion{}, err
		}
		if manages {
			refused = append(refused, c.Name)
		}
	}
	if len(refused) > 0 {
		return stateDeletion{}, &StateDeletionError{Components: refused}
	}
	return del, nil
}

// stateManagesResources reports whether a managed-backend component's state
// may still track resources: its latest recorded state lists some (see
// managesResources) — or nothing was ever recorded but state was written,
// as by an apply that created resources and then failed. The cautious
// answer either way.
func (s *Service) stateManagesResources(ctx context.Context, orgID, componentID uuid.UUID, states []*ent.TofuState) (bool, error) {
	rec, err := s.latestRecordedState(ctx, orgID, componentID)
	if err != nil {
		return false, err
	}
	if rec != nil {
		return rec.Resources != emptyTofuResources, nil
	}
	for _, st := range states {
		if st.CurrentVersion > 0 {
			return true, nil
		}
	}
	return false, nil
}

// deleteStates deletes the managed state rows checkStateDeletions settled on
// (their versions cascade), inside the save's transaction. The delete skips
// a locked row, so a lock taken since the check refuses the save rather
// than deleting state from under a running step.
func deleteStates(ctx context.Context, tx *ent.Tx, orgID, appID uuid.UUID, del stateDeletion) error {
	if len(del.components) == 0 {
		return nil
	}
	n, err := tx.TofuState.Delete().
		Where(
			tstate.OrganizationID(orgID),
			tstate.ApplicationID(appID),
			tstate.ComponentIDIn(del.components...),
			tstate.LockIDIsNil(),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n != del.rows {
		return fmt.Errorf("%w: a removed component's managed state was locked while saving; try again", ErrStateLocked)
	}
	return nil
}
