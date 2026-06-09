package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApplicationGroup is a top-level folder for organizing an organization's
// applications. It behaves like a directory: a group holds applications (via the
// applications.group_id FK), an application belongs to at most one group, and
// groups do not nest. It is purely an organizational convenience — nothing about
// deployment, targeting, or the workflow depends on it.
//
// Like every resource it carries organization_id so every service query is
// org-scoped (the tenancy boundary). Group names are unique within an org.
//
// ON DELETE: the hand-written SQL migration in db/migrations is the source of
// truth for foreign-key delete behavior, not these edges. The organization edge
// is ON DELETE CASCADE there (a group disappears with its org); the
// applications.group_id FK is ON DELETE SET NULL (deleting a group ungroups its
// applications rather than deleting them). ent auto-migrate is never run here, so
// the edge annotations exist only to generate the Go client (column names, types,
// loaders), not the DDL.
type ApplicationGroup struct {
	ent.Schema
}

func (ApplicationGroup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration. Immutable: a group
		// belongs to one org for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ApplicationGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (ApplicationGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Group names are unique within an organization.
		index.Fields("organization_id", "name").Unique(),
	}
}
