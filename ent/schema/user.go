package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// User is a local account record provisioned just-in-time from the OIDC
// identity (Dex). It is keyed by the token subject (oidc_subject); email is
// kept in sync on each login. Organization membership hangs off this via the
// Membership join entity.
type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("oidc_subject").NotEmpty().Unique().Immutable(),
		field.String("email"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		// User ↔ Organization is many-to-many through Membership, which carries
		// the per-org role.
		edge.To("organizations", Organization.Type).
			Through("memberships", Membership.Type),
	}
}
