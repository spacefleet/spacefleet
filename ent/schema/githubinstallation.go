package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// GitHubInstallation records an organization's installation of the operator's
// GitHub App, so the org can deploy charts from private Git repositories. It
// stores only the numeric GitHub installation_id plus the account it is
// installed on (login + type) for display — no secret. At rollout time the
// backend mints a short-lived installation access token from the operator's App
// private key (see lib/githubapp), so nothing credential-bearing lives here.
//
// Surfaced in the UI under "GitHub". The org FK cascades; the
// applications.github_installation_id FK is RESTRICT, so an installation in use
// by an app can't be deleted out from under it (a 409 conflict).
type GitHubInstallation struct {
	ent.Schema
}

// Annotations pins the table name. ent would otherwise derive
// "git_hub_installations" from the type name; the hand-written migration (and
// the FK on applications) uses "github_installations", so this keeps the two in
// sync — without it the client would query a table the migration never creates.
func (GitHubInstallation) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "github_installations"},
	}
}

func (GitHubInstallation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the organization edge below, so the column name is
		// explicit and matches the hand-written migration.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		// The numeric GitHub App installation id. Immutable: a re-install yields a
		// new id, recorded as a new row (or upserted on the unique index below).
		field.Int64("installation_id").Immutable(),
		// The GitHub account (org or user) the App is installed on, and its type
		// ("Organization"/"User"). Non-secret display detail, captured on connect.
		field.String("account_login").Optional(),
		field.String("account_type").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (GitHubInstallation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (GitHubInstallation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// One row per installation within an organization (so a repeated connect
		// callback upserts rather than duplicates).
		index.Fields("organization_id", "installation_id").Unique(),
	}
}
