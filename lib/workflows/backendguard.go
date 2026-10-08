package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
)

// ErrBackendChange is returned by a workflow save that would move an
// OpenTofu component that still manages resources to a different state
// backend. A handler maps it to 409 so the editor can ask for confirmation
// (ReplaceOptions.AllowBackendChange).
var ErrBackendChange = errors.New("workflows: state backend change")

// BackendChangeError names the components whose backend change was refused.
type BackendChangeError struct {
	// Components are the refused components, by name, in workflow order.
	Components []string
}

func (e *BackendChangeError) Error() string {
	return fmt.Sprintf("state backend change for %s: the current state still lists resources and the new backend starts empty, so the next run would plan to create everything again (destroy first, or confirm the switch)",
		strings.Join(e.Components, ", "))
}

func (e *BackendChangeError) Unwrap() error { return ErrBackendChange }

// checkBackendChanges refuses a save that switches the state backend of an
// OpenTofu component whose recorded state still lists resources. A switch
// in any direction makes the next run init against an empty backend and
// plan to create everything the old state tracked — real infrastructure
// would be duplicated or collide. A component that never applied, or whose
// last recorded state is empty (destroyed), switches freely. Moving state
// between backends is a separate, explicit operation.
func (s *Service) checkBackendChanges(ctx context.Context, orgID, appID uuid.UUID, nodes []ComponentInput) error {
	existing, err := s.ent.Component.Query().
		Where(
			component.OrganizationID(orgID),
			component.ApplicationID(appID),
			component.TypeEQ(component.TypeTerraform),
		).
		All(ctx)
	if err != nil {
		return err
	}
	backendOf := make(map[uuid.UUID]string, len(existing))
	for _, c := range existing {
		backendOf[c.ID] = c.Config[terraformConfigBackend]
	}
	var refused []string
	for _, n := range nodes {
		if n.Type != TypeTerraform {
			continue
		}
		old, ok := backendOf[n.ID]
		if !ok || old == n.Config[terraformConfigBackend] {
			continue
		}
		manages, err := s.managesResources(ctx, orgID, appID, n.ID)
		if err != nil {
			return err
		}
		if manages {
			refused = append(refused, n.Name)
		}
	}
	if len(refused) > 0 {
		return &BackendChangeError{Components: refused}
	}
	return nil
}

// managesResources reports whether an OpenTofu component's latest recorded
// state (from a succeeded apply or state operation of this application)
// still lists resources. A record whose inventory was not captured counts
// as managing resources — the cautious answer.
func (s *Service) managesResources(ctx context.Context, orgID, appID, componentID uuid.UUID) (bool, error) {
	cr, err := s.ent.ComponentRun.Query().
		Where(
			componentrun.OrganizationID(orgID),
			componentrun.ComponentIDIn(deriveApplyID(componentID), componentID),
			componentrun.StatusEQ(componentrun.StatusSucceeded),
			componentrun.Or(componentrun.OutputsNEQ(""), componentrun.ResourcesNEQ("")),
			componentrun.HasWorkflowRunWith(
				workflowrun.OrganizationID(orgID),
				workflowrun.ApplicationID(appID),
			),
		).
		Order(ent.Desc(componentrun.FieldFinishedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return cr.Resources != emptyTofuResources, nil
}
