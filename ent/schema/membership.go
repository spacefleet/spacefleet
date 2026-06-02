package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Membership is the join entity binding a User to an Organization, carrying
// the user's role within that organization. Roles are hierarchical —
// admin > editor > viewer: an admin manages the organization itself (members,
// invites, renaming), an editor can take any action within the app, and a
// viewer is read-only. The creator of an organization becomes its first admin;
// everyone else's role comes from the invitation they accepted. It is the only
// place multi-org membership is recorded.
type Membership struct {
	ent.Schema
}

func (Membership) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to edges below, so the column names are explicit and
		// match the hand-written migration.
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		// Least privilege by default; the creator is promoted to admin and
		// invited users get the role their invitation carried.
		field.Enum("role").Values("admin", "editor", "viewer").Default("viewer"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Membership) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Field("user_id").
			Unique().
			Required().
			Immutable(),
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (Membership) Indexes() []ent.Index {
	return []ent.Index{
		// A user can belong to a given organization at most once.
		index.Fields("user_id", "organization_id").Unique(),
	}
}
