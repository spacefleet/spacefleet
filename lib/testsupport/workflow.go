//go:build integration

package testsupport

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
)

// Stage returns the id of an application's first workflow stage, creating it
// (named "Deploy") when the application has none yet. A component can't exist
// outside a stage, so a test that creates components directly — rather than
// through the workflow service — puts them here.
func Stage(t testing.TB, client *ent.Client, orgID, appID uuid.UUID) uuid.UUID {
	t.Helper()
	st, err := client.WorkflowStage.Query().
		Where(workflowstage.OrganizationID(orgID), workflowstage.ApplicationID(appID)).
		Order(ent.Asc(workflowstage.FieldOrdinal)).
		First(context.Background())
	if err == nil {
		return st.ID
	}
	if !ent.IsNotFound(err) {
		t.Fatalf("find workflow stage: %v", err)
	}
	return NewStage(t, client, orgID, appID, "Deploy")
}

// NewStage appends a workflow stage after an application's last one and
// returns its id. A component placed in it runs after every component of the
// earlier stages — how a test orders one component after another.
func NewStage(t testing.TB, client *ent.Client, orgID, appID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	n, err := client.WorkflowStage.Query().
		Where(workflowstage.OrganizationID(orgID), workflowstage.ApplicationID(appID)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count workflow stages: %v", err)
	}
	if name == "" {
		name = fmt.Sprintf("Stage %d", n+1)
	}
	st, err := client.WorkflowStage.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(name).
		SetOrdinal(n).
		Save(ctx)
	if err != nil {
		t.Fatalf("create workflow stage: %v", err)
	}
	return st.ID
}
