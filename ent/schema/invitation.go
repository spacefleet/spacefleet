package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Invitation is a pending request for someone to join an organization with a
// given role. The token is the bearer secret embedded in the invite link:
// whoever holds a valid, unexpired link and is signed in joins with the
// invitation's role (the recorded email is for display and email delivery, not
// an access check). Only a hash of the token is stored. Invitations expire 30
// days after creation and are marked accepted/revoked rather than deleted so
// an admin can see the history.
type Invitation struct {
	ent.Schema
}

func (Invitation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		// The address the invite is intended for (shown to admins, used as the
		// email recipient). Not an authorization check — see the type comment.
		field.String("email").NotEmpty(),
		// Role the invitee receives on accept. Mirrors Membership.role.
		field.Enum("role").Values("admin", "editor", "viewer").Default("viewer"),
		// SHA-256 hex of the random URL-safe bearer token embedded in the invite
		// link. Only the hash is stored — the plaintext token exists only in the
		// link returned at creation time, so a database read can't yield live
		// invite links. Lookups hash the presented token.
		field.String("token_hash").Unique().Immutable().Sensitive(),
		field.Enum("status").Values("pending", "accepted", "revoked").Default("pending"),
		field.Time("expires_at"),
		field.Time("accepted_at").Optional().Nillable(),
		// Audit: the user who created the invitation (optional — the inviter may
		// have since been removed).
		field.UUID("invited_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Invitation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (Invitation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
	}
}
