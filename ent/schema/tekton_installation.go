package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TektonInstallation records that a cluster has been designated to run jobs
// (CI/CD, Helm releases, …) backed by Tekton, plus the lifecycle of installing
// Tekton into it. It is 1:1 with a Cluster (the cluster is the tenancy
// boundary, reached org-scoped via the clusters service) and is deliberately
// separate from the cluster row: install is an asynchronous lifecycle with its
// own status/version/message and the id of the in-flight install job, which
// would otherwise conflate with the cluster's connectivity-probe status.
type TektonInstallation struct {
	ent.Schema
}

func (TektonInstallation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// FK column bound to the cluster edge below; explicit so the column name
		// matches the hand-written migration. Immutable: the installation belongs
		// to one cluster for its lifetime.
		field.UUID("cluster_id", uuid.UUID{}).Immutable(),
		// Whether the operator has designated this cluster to run jobs. A row
		// exists once Tekton has been touched for the cluster; enabled is the
		// opt-in flag.
		field.Bool("enabled").Default(false),
		// The install lifecycle:
		//   not_installed — Tekton is absent (or was uninstalled)
		//   installing    — an install job is applying the Tekton release
		//   installed     — Tekton is present and the controller is ready
		//   upgrading     — re-applying a newer pinned release
		//   failed        — the last install/upgrade job errored (see status_message)
		//   uninstalling  — an uninstall job is removing Tekton
		field.Enum("status").
			Values("not_installed", "installing", "installed", "upgrading", "failed", "uninstalling").
			Default("not_installed"),
		// The pinned Tekton release version applied by the last successful install.
		field.String("installed_version").Optional(),
		// Human-readable detail: the last install-progress line, or the error when
		// status is failed.
		field.String("status_message").Optional(),
		// River job id of the in-flight install/uninstall, for UI correlation.
		field.String("job_id").Optional(),
		// Timestamp of the most recent presence detection.
		field.Time("last_checked_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TektonInstallation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("cluster", Cluster.Type).
			Ref("tekton").
			Field("cluster_id").
			Unique().
			Required().
			Immutable(),
	}
}

func (TektonInstallation) Indexes() []ent.Index {
	return []ent.Index{
		// One Tekton installation per cluster.
		index.Fields("cluster_id").Unique(),
	}
}
