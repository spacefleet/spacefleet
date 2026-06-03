package tekton

import (
	"context"
	"strconv"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// Install lifecycle status values. They match the tekton_installation.status
// ent enum; the worker reports progress through Store.MarkTekton using these so
// the persistence layer (lib/clusters) maps them to the column without the
// worker importing it (which would be a cycle — lib/clusters imports lib/tekton).
const (
	StatusNotInstalled = "not_installed"
	StatusInstalling   = "installing"
	StatusInstalled    = "installed"
	StatusUpgrading    = "upgrading"
	StatusFailed       = "failed"
	StatusUninstalling = "uninstalling"
)

// Actions an install job can carry. Install and Upgrade run the same idempotent
// server-side apply (it converges, so re-applying a newer pinned release is the
// upgrade); they differ only in the lifecycle status reported while in flight.
const (
	ActionInstall   = "install"
	ActionUpgrade   = "upgrade"
	ActionUninstall = "uninstall"
)

// Store is the persistence the install worker needs, satisfied by
// *clusters.Service. Defining it here (rather than importing lib/clusters)
// keeps the dependency one-way: lib/clusters imports lib/tekton, the worker is
// handed a Store at construction in cmd/spacefleet/worker.go. The worker passes
// the org id it received in the job args so every call stays org-scoped.
type Store interface {
	// ConnForTekton resolves the (decrypted) connection for an org-scoped cluster.
	ConnForTekton(ctx context.Context, orgID, clusterID uuid.UUID) (k8s.Connection, error)
	// MarkTekton persists the install lifecycle: status is one of the Status*
	// constants; version/message are set as appropriate (empty to clear).
	MarkTekton(ctx context.Context, orgID, clusterID uuid.UUID, jobID, status, version, message string) error
}

// InstallArgs is the River job enqueued by the API when an org enables (or
// disables) job-running on a cluster. It carries only ids — never credentials,
// which would otherwise sit in cleartext in the job row; the worker re-opens
// sealed credentials through the Store.
type InstallArgs struct {
	ClusterID uuid.UUID `json:"cluster_id"`
	OrgID     uuid.UUID `json:"org_id"`
	Action    string    `json:"action"`
}

// Kind is the stable River job identifier.
func (InstallArgs) Kind() string { return "tekton_install" }

// InstallWorker installs (or uninstalls) Tekton in a cluster, recording progress
// through the Store. It owns credential access via the Store exactly as the
// invite-email worker owns its Sender — the request path never runs the install.
type InstallWorker struct {
	river.WorkerDefaults[InstallArgs]
	Store Store

	// installFn/uninstallFn default to Install/Uninstall; tests override them to
	// drive Work without a live cluster.
	installFn   func(ctx context.Context, conn k8s.Connection, progress func(string)) (string, error)
	uninstallFn func(ctx context.Context, conn k8s.Connection, progress func(string)) error
}

func (w *InstallWorker) install(ctx context.Context, conn k8s.Connection, progress func(string)) (string, error) {
	if w.installFn != nil {
		return w.installFn(ctx, conn, progress)
	}
	return Install(ctx, conn, progress)
}

func (w *InstallWorker) uninstall(ctx context.Context, conn k8s.Connection, progress func(string)) error {
	if w.uninstallFn != nil {
		return w.uninstallFn(ctx, conn, progress)
	}
	return Uninstall(ctx, conn, progress)
}

// Work resolves the cluster's connection, then applies (or removes) the pinned
// Tekton release via server-side apply, persisting status transitions as it
// goes. Returning a non-nil error lets River retry with backoff; the apply is
// idempotent (server-side apply converges), so retries are safe.
func (w *InstallWorker) Work(ctx context.Context, job *river.Job[InstallArgs]) error {
	a := job.Args
	jobID := strconv.FormatInt(job.ID, 10)

	conn, err := w.Store.ConnForTekton(ctx, a.OrgID, a.ClusterID)
	if err != nil {
		_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusFailed, "", err.Error())
		return err
	}

	if a.Action == ActionUninstall {
		_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusUninstalling, "", "removing Tekton")
		if err := w.uninstall(ctx, conn, nil); err != nil {
			_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusFailed, "", err.Error())
			return err
		}
		return w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusNotInstalled, "", "")
	}

	// Install and upgrade share the apply; only the in-flight status differs.
	inFlight, start := StatusInstalling, "starting Tekton install"
	if a.Action == ActionUpgrade {
		inFlight, start = StatusUpgrading, "starting Tekton upgrade"
	}
	_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, inFlight, "", start)
	version, err := w.install(ctx, conn, func(line string) {
		_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, inFlight, "", line)
	})
	if err != nil {
		_ = w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusFailed, "", err.Error())
		return err
	}
	return w.Store.MarkTekton(ctx, a.OrgID, a.ClusterID, jobID, StatusInstalled, version, "")
}
