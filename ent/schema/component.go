package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Component is one step of an application's deploy workflow — a typed job that
// runs within one of the application's stages. The first types are a Helm release ("helm") and a
// manifest apply ("manifest"); the enum lets the set grow (Terraform, …) without
// a migration churn. Type-specific, non-secret parameters live in config, a flat
// string map validated per type in the service (helm: chart_source/repo_url/
// chart/version/git_ref/git_path/values/values_sources/release_name; manifest:
// repo_url/git_ref/path), mirroring how Application stored its chart config.
//
// stage_id is the stage the component runs in and ordinal its display order
// within that stage; the components of a stage run in parallel and stages run in
// order (see WorkflowStage). continue_on_failure decides whether a failed
// component skips the later stages (false) or not (true → the run goes
// "partial"). target_cluster_id /
// target_namespace are the component's own deploy target — required for the
// cluster-deploying types (helm needs both, manifest needs the cluster) and
// unset for terraform, which has no cluster target; the service validates this
// per type. chart_credential_id / github_installation_id are optional
// private-pull credentials.
//
// Like every resource it carries organization_id so every service query is
// org-scoped (the tenancy boundary), not via the application join alone.
//
// ON DELETE: the hand-written SQL migration in db/migrations is the source of
// truth for foreign-key delete behavior, not these edges. ent's auto-migration
// would emit ON DELETE SET NULL for the optional edges below (target_cluster,
// chart_credential, github_installation); the migration deliberately uses
// RESTRICT instead, so a cluster or credential a component points at can't be
// deleted out from under an in-flight or historical workflow. That divergence is
// intentional and harmless: ent auto-migrate is never run here — migrations are
// applied only by the `migrate` subcommand from the SQL files — so the schema
// the database actually enforces is the migration's, and these edge annotations
// exist only to generate the Go client (column names, types, loaders), not the
// DDL.
type Component struct {
	ent.Schema
}

// ApprovalPolicy is the per-component approval policy applied whenever the
// node parks at its approval gate (requires_approval). The zero value is the
// pre-existing behaviour: any editor or admin may approve, one approval opens
// the gate, the person who started the run may approve it, and a parked run
// waits forever. Stored as one JSON column so the policy can grow without
// migrations.
type ApprovalPolicy struct {
	// Approvers lists who may approve, by email (lowercased). Empty means any
	// editor or admin of the organization.
	Approvers []string `json:"approvers,omitempty"`
	// Required is how many distinct approvals open the gate (N-of-M). 0 and 1
	// both mean one.
	Required int `json:"required,omitempty"`
	// RequireDifferentApprover forbids the person who started the run from
	// approving it (no self-approval).
	RequireDifferentApprover bool `json:"require_different_approver,omitempty"`
	// TimeoutMinutes fails the run when the gate has waited this long without
	// a decision. 0 waits forever.
	TimeoutMinutes int `json:"timeout_minutes,omitempty"`
}

func (Component) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK columns bound to the edges below; explicit so the column names match
		// the hand-written migration. Both immutable: a component belongs to one org
		// and one application for its lifetime.
		field.UUID("organization_id", uuid.UUID{}).Immutable(),
		field.UUID("application_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		// The component type: a Helm release, a manifest apply, or an OpenTofu
		// plan/apply. The enum lets the set grow without a migration for the common
		// case (the components.type column is unconstrained TEXT).
		field.Enum("type").
			Values("helm", "manifest", "terraform").
			Default("helm"),
		// Non-secret, type-specific parameters. Stored as a flat string map so the
		// shape can vary per type without a migration, exactly as Application stored
		// its chart config. Validated per type in the service.
		field.JSON("config", map[string]string{}).Optional(),
		// The stage this component runs in. Bound to the stage edge below; ON
		// DELETE CASCADE in the migration.
		field.UUID("stage_id", uuid.UUID{}),
		// Display order within the stage, 0-based. Rewritten on every workflow
		// replace. Order carries no run semantics — a stage's components run in
		// parallel.
		field.Int("ordinal").Default(0),
		// When true, a failure of this component does not skip the later stages or
		// fail the run (the run settles "partial" instead).
		field.Bool("continue_on_failure").Default(false),
		// When true, a run pauses at this node ("awaiting_approval") and waits for a
		// human to approve before it executes. The general per-component approval-gate
		// flag; OpenTofu's apply node is its first consumer.
		field.Bool("requires_approval").Default(false),
		// The policy applied at that gate (who, how many, self-approval, a
		// timeout). The zero value keeps the original any-editor, one-approval
		// behaviour. See ApprovalPolicy.
		field.JSON("approval_policy", ApprovalPolicy{}).Optional(),
		// The cluster this component deploys into. Required for helm/manifest,
		// unset for terraform (validated per type in the service). Optional at the
		// schema/column level because terraform leaves it unset. Bound to the
		// target_cluster edge below; RESTRICT in the migration.
		field.UUID("target_cluster_id", uuid.UUID{}).Optional(),
		// The namespace this component deploys into. Required for helm, unused for
		// manifest/terraform (validated per type in the service).
		field.String("target_namespace").Optional(),
		// Optional credential for pulling a private chart, mirroring Application.
		// Bound to the chart_credential edge below; RESTRICT in the migration.
		field.UUID("chart_credential_id", uuid.UUID{}).Optional(),
		// Optional GitHub App installation for pulling a private git source,
		// mirroring Application. Bound to the github_installation edge below;
		// RESTRICT in the migration.
		field.UUID("github_installation_id", uuid.UUID{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Component) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("organization", Organization.Type).
			Field("organization_id").
			Unique().
			Required().
			Immutable(),
		// The application this component belongs to. ON DELETE CASCADE in the
		// migration: a component disappears with its application.
		edge.To("application", Application.Type).
			Field("application_id").
			Unique().
			Required().
			Immutable(),
		// Optional per-component target cluster override. RESTRICT in the migration:
		// a cluster a component targets can't be deleted out from under it.
		edge.To("target_cluster", Cluster.Type).
			Field("target_cluster_id").
			Unique(),
		// Optional credential for pulling a private chart. RESTRICT in the migration.
		edge.To("chart_credential", ChartCredential.Type).
			Field("chart_credential_id").
			Unique(),
		// Optional GitHub App installation for pulling a private git source.
		// RESTRICT in the migration.
		edge.To("github_installation", GitHubInstallation.Type).
			Field("github_installation_id").
			Unique(),
		// The stage this component runs in. ON DELETE CASCADE in the migration: a
		// component disappears with its stage.
		edge.To("stage", WorkflowStage.Type).
			Field("stage_id").
			Unique().
			Required(),
	}
}

func (Component) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id"),
		// Listing an application's components.
		index.Fields("application_id"),
		// Listing a stage's components.
		index.Fields("stage_id"),
	}
}
