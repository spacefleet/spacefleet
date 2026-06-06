package clusters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/tektoninstallation"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
)

// Precondition errors for the upgrade/uninstall lifecycle, returned by the
// Begin* methods for the handler to map to HTTP status. errNotManaged guards
// the safety boundary: we only ever upgrade or remove a Tekton this app
// installed (detected via its ownership label), never one the cluster owner
// installed themselves.
var (
	errNotManaged   = errors.New("clusters: tekton in this cluster was not installed by spacefleet")
	errNotInstalled = errors.New("clusters: tekton is not installed in this cluster")
)

// ErrTektonNotManaged reports whether err is the not-managed precondition.
func ErrTektonNotManaged(err error) bool { return errors.Is(err, errNotManaged) }

// ErrTektonNotInstalled reports whether err is the not-installed precondition.
func ErrTektonNotInstalled(err error) bool { return errors.Is(err, errNotInstalled) }

// This file holds the cluster service's Tekton (job-running) use cases. Every
// method resolves the cluster org-scoped first (s.Get), so the
// tekton_installation row — reached by cluster_id — inherits the cluster's
// tenancy boundary; the row carries no organization_id of its own.

// ConnForTekton resolves the decrypted connection for an org-scoped cluster. It
// is the worker-facing entry point (the install worker holds the service as a
// tekton.Store) and the basis for the run methods below.
func (s *Service) ConnForTekton(ctx context.Context, orgID, clusterID uuid.UUID) (k8s.Connection, error) {
	c, err := s.Get(ctx, orgID, clusterID)
	if err != nil {
		return k8s.Connection{}, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return k8s.Connection{}, err
	}
	return k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	}, nil
}

// TektonRow returns the cluster's Tekton installation row, scoped to the org. If
// no row exists yet it returns a non-persisted default (not_installed,
// disabled) — a cheap read that never writes, suitable for the install stream's
// tail.
func (s *Service) TektonRow(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, error) {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	row, err := s.ent.TektonInstallation.Query().
		Where(tektoninstallation.ClusterID(id)).
		Only(ctx)
	if err == nil {
		return row, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return &ent.TektonInstallation{ClusterID: id, Status: tektoninstallation.StatusNotInstalled}, nil
}

// TektonStatus returns the installation row plus a live presence detection,
// reconciling the two: a cluster found installed-and-ready is recorded as
// installed; one found absent (and not mid-install) is recorded as
// not_installed. A detection failure is returned to the caller (the row is not
// clobbered), matching Nodes/Capabilities.
func (s *Service) TektonStatus(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, *tekton.Presence, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	row, err := s.ensureTektonRow(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	presence, derr := tekton.Detect(ctx, conn)
	if derr != nil {
		return row, nil, derr
	}
	row, err = s.reconcileTekton(ctx, row, presence)
	if err != nil {
		return nil, nil, err
	}
	return row, presence, nil
}

// EnableTekton designates a cluster to run jobs. It sets enabled and, when
// Tekton isn't already installed, moves the row to installing so the handler can
// enqueue an install job. Idempotent: enabling an already-installed cluster just
// keeps enabled true.
func (s *Service) EnableTekton(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, error) {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	row, err := s.ensureTektonRow(ctx, id)
	if err != nil {
		return nil, err
	}
	upd := row.Update().SetEnabled(true)
	if row.Status == tektoninstallation.StatusNotInstalled || row.Status == tektoninstallation.StatusFailed {
		upd.SetStatus(tektoninstallation.StatusInstalling).SetStatusMessage("queued for install")
	}
	return upd.Save(ctx)
}

// DisableTekton stops designating a cluster as a job runner. It does not
// uninstall Tekton (that is a separate, explicit action) — it only clears the
// opt-in flag.
func (s *Service) DisableTekton(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, error) {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	row, err := s.ensureTektonRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return row.Update().SetEnabled(false).Save(ctx)
}

// BeginTektonUpgrade validates that a Spacefleet-managed Tekton is installed,
// then moves the row to upgrading so the handler can enqueue an upgrade job. It
// returns errNotManaged if the cluster's Tekton wasn't installed by Spacefleet
// and errNotInstalled if Tekton is absent; a detection failure (unreachable
// cluster) is returned as-is. The upgrade re-applies the pinned release, which
// also repairs a failed/partial install.
func (s *Service) BeginTektonUpgrade(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, error) {
	presence, err := s.detectForLifecycle(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !presence.Installed {
		return nil, errNotInstalled
	}
	if !presence.Managed {
		return nil, errNotManaged
	}
	row, err := s.ensureTektonRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return row.Update().
		SetStatus(tektoninstallation.StatusUpgrading).
		SetStatusMessage("queued for upgrade").
		Save(ctx)
}

// BeginTektonUninstall validates that a Spacefleet-managed Tekton is present,
// then moves the row to uninstalling and clears enabled (removing Tekton
// un-designates the cluster as a job runner) so the handler can enqueue an
// uninstall job. It returns errNotManaged if the cluster's Tekton wasn't
// installed by Spacefleet; a detection failure is returned as-is.
func (s *Service) BeginTektonUninstall(ctx context.Context, orgID, id uuid.UUID) (*ent.TektonInstallation, error) {
	presence, err := s.detectForLifecycle(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !presence.Managed {
		return nil, errNotManaged
	}
	row, err := s.ensureTektonRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return row.Update().
		SetEnabled(false).
		SetStatus(tektoninstallation.StatusUninstalling).
		SetStatusMessage("queued for removal").
		Save(ctx)
}

// detectForLifecycle resolves the org-scoped cluster connection and runs a live
// presence detection — the shared preamble for the upgrade/uninstall Begin
// methods, which gate on whether Tekton is present and Spacefleet-managed.
func (s *Service) detectForLifecycle(ctx context.Context, orgID, id uuid.UUID) (*tekton.Presence, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	detect := s.detectTekton
	if detect == nil {
		detect = tekton.Detect
	}
	return detect(ctx, conn)
}

// MarkTekton persists an install-lifecycle transition reported by the install
// worker (it satisfies tekton.Store). status must be one of the tekton.Status*
// values; jobID/version are set only when non-empty, message is always set
// (pass "" to clear). The org id the worker received in the job args still flows
// through s.Get, so the write stays org-scoped.
func (s *Service) MarkTekton(ctx context.Context, orgID, clusterID uuid.UUID, jobID, status, version, message string) error {
	if _, err := s.Get(ctx, orgID, clusterID); err != nil {
		return err
	}
	st, err := tektonStatusEnum(status)
	if err != nil {
		return err
	}
	row, err := s.ensureTektonRow(ctx, clusterID)
	if err != nil {
		return err
	}
	upd := row.Update().SetStatus(st).SetStatusMessage(message)
	if jobID != "" {
		upd.SetJobID(jobID)
	}
	if version != "" {
		upd.SetInstalledVersion(version)
	}
	_, err = upd.Save(ctx)
	return err
}

// SubmitRun submits a minimal TaskRun to an org-scoped cluster and returns its
// initial status. The run is stamped with the owning org's id label so that two
// organizations sharing the same physical runner cluster are scoped apart: the
// Get/Watch lookups below require a matching label, so one org can't resolve
// another's run by name and read its status or pod logs.
func (s *Service) SubmitRun(ctx context.Context, orgID, id uuid.UUID, namespace string, spec tekton.RunSpec) (*tekton.RunStatus, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	spec.Labels = withOrgLabel(spec.Labels, orgID)
	return tekton.SubmitTaskRun(ctx, conn, namespace, spec)
}

// GetRun fetches one TaskRun's status for an org-scoped cluster. The lookup is
// gated on the run's org-id label, so a run another org submitted on the same
// physical runner is not disclosed (tekton.ErrRunForbidden).
func (s *Service) GetRun(ctx context.Context, orgID, id uuid.UUID, namespace, name string) (*tekton.RunStatus, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return tekton.GetRunScoped(ctx, conn, namespace, name, orgID.String())
}

// WatchRun opens a live watch on a TaskRun for an org-scoped cluster. Like
// GetRun, the initial lookup is gated on the run's org-id label.
func (s *Service) WatchRun(ctx context.Context, orgID, id uuid.UUID, namespace, name string) (*tekton.RunStream, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return tekton.WatchRunScoped(ctx, conn, namespace, name, orgID.String())
}

// RunLogs streams a run's pod logs for an org-scoped cluster, delegating to the
// shared pod-log streamer.
//
// NOTE (follow-up): this lookup is keyed by pod name, not run name, so it can't
// reuse the org-id label predicate the Get/Watch lookups above use without first
// resolving the pod back to its owning TaskRun. The labeling is in place (every
// run and its pod carry the org-id label); threading a pod->run org check here
// is left as a follow-up to avoid a broad change to the pod-log seam.
func (s *Service) RunLogs(ctx context.Context, orgID, id uuid.UUID, namespace, podName string, opts k8s.LogOptions) (io.ReadCloser, error) {
	conn, err := s.ConnForTekton(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return k8s.StreamPodLogs(ctx, conn, namespace, podName, opts)
}

// withOrgLabel returns labels with RunOrgLabel set to the org id, allocating a
// new map when needed so the caller's spec.Labels isn't mutated underneath it.
func withOrgLabel(labels map[string]string, orgID uuid.UUID) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[tekton.RunOrgLabel] = orgID.String()
	return out
}

// ensureTektonRow returns the cluster's installation row, creating a default
// (not_installed, disabled) row if none exists. Callers must have verified org
// scope via s.Get first.
func (s *Service) ensureTektonRow(ctx context.Context, clusterID uuid.UUID) (*ent.TektonInstallation, error) {
	row, err := s.ent.TektonInstallation.Query().
		Where(tektoninstallation.ClusterID(clusterID)).
		Only(ctx)
	if err == nil {
		return row, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return s.ent.TektonInstallation.Create().SetClusterID(clusterID).Save(ctx)
}

// reconcileTekton records what a live detection found, without clobbering an
// in-flight install: an installed+ready cluster becomes installed; an absent one
// becomes not_installed unless a job is mid-install. last_checked_at is always
// bumped.
func (s *Service) reconcileTekton(ctx context.Context, row *ent.TektonInstallation, p *tekton.Presence) (*ent.TektonInstallation, error) {
	upd := row.Update().SetLastCheckedAt(time.Now())
	switch {
	case p.Installed && p.ControllerReady:
		if row.Status != tektoninstallation.StatusInstalled {
			upd.SetStatus(tektoninstallation.StatusInstalled).SetStatusMessage("")
		}
		if v := tektonVersion(p); v != "" {
			upd.SetInstalledVersion(v)
		}
	case !p.Installed:
		// Tekton is absent. If we thought it was installed it drifted/was removed;
		// reflect that. While a job is installing, leave the status alone.
		if row.Status == tektoninstallation.StatusInstalled {
			upd.SetStatus(tektoninstallation.StatusNotInstalled).SetInstalledVersion("")
		}
	}
	return upd.Save(ctx)
}

// tektonVersion prefers the detected running version, falling back to the pinned
// version Spacefleet installs.
func tektonVersion(p *tekton.Presence) string {
	if p.Version != "" {
		return p.Version
	}
	return tekton.PinnedVersion
}

// tektonStatusEnum validates and converts a tekton.Status* string to the ent
// enum, rejecting unknown values so a typo can't reach the column.
func tektonStatusEnum(s string) (tektoninstallation.Status, error) {
	st := tektoninstallation.Status(s)
	switch st {
	case tektoninstallation.StatusNotInstalled,
		tektoninstallation.StatusInstalling,
		tektoninstallation.StatusInstalled,
		tektoninstallation.StatusUpgrading,
		tektoninstallation.StatusFailed,
		tektoninstallation.StatusUninstalling:
		return st, nil
	default:
		return "", fmt.Errorf("clusters: unknown tekton status %q", s)
	}
}
