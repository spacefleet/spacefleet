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
// the user's role within that organization. The creator of an organization
// becomes its "owner"; everyone else is a "member". It is the only place
// multi-org membership is recorded.
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
		field.Enum("role").Values("owner", "member").Default("member"),
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
