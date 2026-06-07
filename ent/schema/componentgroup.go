package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ComponentGroup is an explicit container in an application's deploy workflow: a
// named box holding several components that run in parallel. It is a
// builder/persistence concept only — the scheduler never sees a group. At
// validation time and at run-snapshot time a group desugars into component-level
// depends_on: a node that depends on a group waits for *all* of the group's
// members (all-must-complete), and a group that depends on Y makes every member
// wait on Y. Members of the same group have no implicit interdependency (they
// run in parallel).
//
// depends_on lists the components/groups the whole group waits on (entries may
// reference a component id OR a sibling group id, mirroring how a component's
// depends_on may reference either). There are no nested groups: a group has no
// group_id and cannot be a member of another group.
//
// Like every resource it carries organization_id so every service query is
// org-scoped (the tenancy boundary), not via the application join alone.
//
// ON DELETE: the hand-written SQL migration in db/migrations is the source of
// truth for foreign-key delete behavior, not these edges. The application edge
// is ON DELETE CASCADE there (a group disappears with its application); the
// components.group_id FK is ON DELETE SET NULL (deleting a group ungroups its
// members rather than deleting them). ent auto-migrate is never run here, so the
// edge annotations exist only to generate the Go client (column names, types,
// loaders), not the DDL.
type ComponentGroup struct {
	ent.Schema
}

func (ComponentGroup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a group belongs to one org
		// and one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// Ids of the components/groups the whole group waits on. Entries may be a
		// component id or a sibling group id; the service desugars these into
		// component-level edges at validate/snapshot time.
		field.JSON("depends_on", []uuid.UUID{}).Optional(),
		// Canvas coordinates {x, y} for the workflow builder UI.
		field.JSON("position", map[string]float64{}).Optional(),
		// Canvas dimensions {w, h} for the workflow builder UI.
		field.JSON("size", map[string]float64{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ComponentGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this group belongs to. ON DELETE CASCADE in the
		// migration: a group disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (ComponentGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's groups.
		index.Fields("application_id"),
	}
}
