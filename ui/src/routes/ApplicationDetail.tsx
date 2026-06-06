import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowLeft,
  ArrowUpCircle,
  CheckCircle2,
  History,
  Loader2,
  Pencil,
  Play,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { AppStatusBadge } from "./Applications";
import { DeploymentStatusBadge } from "./DeploymentDetail";
import { DeleteApplicationDialog } from "../components/DeleteApplicationDialog";
import { DeployConfirmDialog } from "../components/DeployConfirmDialog";
import { chartSourceLabel } from "../components/chartSources";
import { formatDuration } from "../lib/duration";
import { useObjectStream } from "../lib/useObjectStream";

type Application = components["schemas"]["Application"];
type Deployment = components["schemas"]["Deployment"];
type SyncStatus = components["schemas"]["SyncStatus"];

// Statuses where a rollout job is in flight — the status/run/log streams are
// open and the UI shows live progress.
const IN_FLIGHT: Application["status"][] = ["deploying", "uninstalling"];

// ApplicationDetail is the single place to see and manage one application
// (route /applications/:appId). It shows the chart config and current rollout
// status, offers Deploy/Upgrade/Edit/Delete, and streams the live rollout
// status, the TaskRun, and the helm logs while a rollout runs. The status
// reflects the outcome of the last rollout (helm --wait), not ongoing release
// health. Deleting an app optionally uninstalls its release first (see the
// delete dialog) — there's no standalone uninstall.
export function ApplicationDetail() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [app, setApp] = useState<Application | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [clusters, setClusters] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState(false);
  // The deploy/upgrade confirmation dialog (shows the diff first), null when closed.
  const [confirm, setConfirm] = useState<"deploy" | "upgrade" | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/applications/{id}", {
      params: { path: { id: appId } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not load this application");
      setApp(null);
      setLoading(false);
      return;
    }
    setApp(data);
    setLoading(false);
  }, [appId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Map cluster ids to names for display.
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      const m: Record<string, string> = {};
      for (const c of data ?? []) m[c.id] = c.name;
      setClusters(m);
    })();
  }, [currentOrg?.id]);

  const inFlight = app ? IN_FLIGHT.includes(app.status) : false;
  // A refresh (preview/diff) job is in flight — the status stream stays open to
  // carry its progress even when the rollout itself has long settled.
  const syncing = app?.sync_status === "refreshing";

  // The deployment history (rollout runs, newest first). Reloaded whenever a
  // rollout starts or settles — keyed below on the app's job id + status, which
  // change exactly on those transitions — so a new run appears and a finishing
  // run flips to its terminal state without polling.
  const loadDeployments = useCallback(async () => {
    const { data } = await api.GET("/api/applications/{id}/deployments", {
      params: { path: { id: appId } },
    });
    setDeployments(data ?? []);
  }, [appId]);
  useEffect(() => {
    void loadDeployments();
  }, [loadDeployments, currentOrg?.id, app?.job_id, app?.status]);

  // Follow the rollout status live while a job is in flight; on settle, the
  // stream delivers the terminal row, which we fold in directly (and the effect
  // above then refreshes the history with the finished run).
  const { value: streamedApp } = useObjectStream<Application>(
    `/api/applications/${appId}/stream`,
    inFlight || syncing,
  );
  useEffect(() => {
    if (streamedApp) setApp(streamedApp);
  }, [streamedApp]);

  // Refresh re-resolves the desired state and diffs it against the live cluster
  // (a preview job), changing nothing. The status stream above then carries the
  // sync_status from refreshing → synced/out_of_sync.
  async function refresh() {
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST("/api/applications/{id}/refresh", {
      params: { path: { id: appId } },
    });
    setBusy(false);
    if (error) setActionError(error.message ?? "Could not start the refresh");
    else if (data) setApp(data);
  }

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/applications")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to applications
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error || !app ? (
        <p className="mt-6 text-sm text-red-600">{error ?? "Not found"}</p>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
                Applications
              </p>
              <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
                {app.name}
              </h1>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <AppStatusBadge status={app.status} message={app.status_message} />
                <SyncBadge status={app.sync_status} message={app.sync_message} />
                {app.last_refreshed_at && !syncing && (
                  <span className="text-xs text-neutral-400">
                    refreshed {new Date(app.last_refreshed_at).toLocaleString()}
                  </span>
                )}
              </div>
            </div>
            {canEdit && (
              <div className="flex flex-wrap items-center justify-end gap-2">
                <button
                  type="button"
                  onClick={() => void refresh()}
                  disabled={busy || inFlight || syncing}
                  title="Re-resolve the desired state and diff it against the live cluster"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  <RefreshCw className={`h-3.5 w-3.5 ${syncing ? "animate-spin" : ""}`} />
                  Refresh
                </button>
                {app.status === "deployed" ? (
                  <button
                    type="button"
                    onClick={() => setConfirm("upgrade")}
                    disabled={busy || inFlight}
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                  >
                    <ArrowUpCircle className="h-3.5 w-3.5" />
                    Upgrade
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => setConfirm("deploy")}
                    disabled={busy || inFlight}
                    className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                  >
                    <Play className="h-3.5 w-3.5" />
                    Deploy
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => navigate(`/applications/${appId}/edit`)}
                  disabled={busy || inFlight}
                  title="Edit this application"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  <Pencil className="h-3.5 w-3.5" />
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => setDeleting(true)}
                  disabled={busy || inFlight}
                  title="Delete this application"
                  className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  Delete
                </button>
              </div>
            )}
          </div>

          {actionError && (
            <p className="mt-3 text-sm text-red-600">{actionError}</p>
          )}
          {app.status_message && (
            <p className="mt-3 text-sm text-neutral-500">{app.status_message}</p>
          )}

          {/* Imported apps haven't yet had their configured chart source verified
              against the live release — prompt a refresh (the import auto-runs
              one, so this typically shows only if that didn't settle). */}
          {app.imported && app.sync_status === "unknown" && (
            <p className="mt-3 border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800">
              Imported from an existing release. Refresh to confirm the chart
              source you configured reproduces what's running on the cluster
              before your first upgrade.
            </p>
          )}

          {/* Configuration */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Configuration
            </h2>
            <dl className="mt-3 grid grid-cols-1 gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
              <Row label="Chart source" value={chartSourceLabel(app.chart_source)} />
              <Row label="Release name" value={app.release_name || app.name} />
              <Row
                label="Target cluster"
                value={clusters[app.target_cluster_id] ?? app.target_cluster_id}
              />
              <Row label="Target namespace" value={app.target_namespace} />
              <Row
                label="Runner cluster"
                value={clusters[app.runner_cluster_id] ?? app.runner_cluster_id}
              />
              {Object.entries(app.config).map(([k, v]) => (
                <Row key={k} label={k} value={v} />
              ))}
            </dl>
          </div>

          {/* Deployment history (rollout runs, newest first) */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex items-center gap-2 border-b border-neutral-200 px-4 py-2">
              <History className="h-3.5 w-3.5 text-neutral-400" />
              <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                Deployments
              </h2>
            </div>
            {deployments.length === 0 ? (
              <p className="px-4 py-6 text-sm text-neutral-500">
                No rollouts yet. Deploy this application to start one.
              </p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                    <th className="px-4 py-2 font-medium">Run</th>
                    <th className="px-4 py-2 font-medium">Action</th>
                    <th className="px-4 py-2 font-medium">Status</th>
                    <th className="px-4 py-2 font-medium">Revision</th>
                    <th className="px-4 py-2 font-medium">Started</th>
                    <th className="px-4 py-2 font-medium">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {deployments.map((d, i) => (
                    <tr
                      key={d.id}
                      onClick={() =>
                        navigate(`/applications/${appId}/deployments/${d.id}`)
                      }
                      className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                    >
                      <td className="px-4 py-3 font-medium text-neutral-900">
                        <Link
                          to={`/applications/${appId}/deployments/${d.id}`}
                          onClick={(e) => e.stopPropagation()}
                          className="hover:underline"
                        >
                          #{deployments.length - i}
                        </Link>
                      </td>
                      <td className="px-4 py-3 capitalize text-neutral-600">
                        {d.action}
                      </td>
                      <td className="px-4 py-3">
                        <DeploymentStatusBadge status={d.status} />
                      </td>
                      <td className="px-4 py-3">
                        <RevisionCell
                          chart={d.chart_revision}
                          values={d.values_revision}
                        />
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {new Date(d.created_at).toLocaleString()}
                      </td>
                      <td className="px-4 py-3 text-neutral-600">
                        {formatDuration(d.created_at, d.finished_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {confirm && (
            <DeployConfirmDialog
              app={app}
              action={confirm}
              onClose={() => setConfirm(null)}
              onConfirmed={(updated) => {
                setApp(updated);
                setConfirm(null);
              }}
            />
          )}

          {deleting && (
            <DeleteApplicationDialog
              app={app}
              onClose={() => setDeleting(false)}
              onDeleted={() => navigate("/applications")}
            />
          )}
        </>
      )}
    </div>
  );
}

// SyncBadge shows whether the application's desired state (re-resolved on
// refresh) matches the live cluster. "unknown" (never refreshed) is hidden so a
// freshly-created app isn't cluttered with a meaningless badge.
function SyncBadge({ status, message }: { status?: SyncStatus; message?: string }) {
  if (!status || status === "unknown") return null;
  const styles: Record<Exclude<SyncStatus, "unknown">, string> = {
    refreshing: "bg-blue-100 text-blue-800",
    synced: "bg-green-100 text-green-800",
    out_of_sync: "bg-amber-100 text-amber-800",
    error: "bg-red-100 text-red-800",
  };
  const label: Record<Exclude<SyncStatus, "unknown">, string> = {
    refreshing: "refreshing",
    synced: "in sync",
    out_of_sync: "out of sync",
    error: "sync error",
  };
  const Icon: Record<Exclude<SyncStatus, "unknown">, typeof CheckCircle2> = {
    refreshing: Loader2,
    synced: CheckCircle2,
    out_of_sync: AlertTriangle,
    error: AlertTriangle,
  };
  const key = status as Exclude<SyncStatus, "unknown">;
  const I = Icon[key];
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[key]}`}
      title={status === "error" ? message : undefined}
    >
      <I className={`h-3.5 w-3.5 ${status === "refreshing" ? "animate-spin" : ""}`} />
      {label[key]}
    </span>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="break-all text-neutral-800">{value || "—"}</dd>
    </div>
  );
}

// RevisionCell shows the git commit SHAs a run resolved, abbreviated. The chart
// is a single SHA; values is one "<repo>@<sha>" line per source. Empty for
// non-git sources (an http_repo/oci chart with no git-sourced values).
function RevisionCell({
  chart,
  values,
}: {
  chart?: string;
  values?: string;
}) {
  // Pull the short SHA from each "<repo>@<sha>" values line.
  const valueShas = (values ?? "")
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const at = line.lastIndexOf("@");
      return (at >= 0 ? line.slice(at + 1) : line).slice(0, 7);
    });
  if (!chart && valueShas.length === 0) {
    return <span className="text-neutral-400">—</span>;
  }
  return (
    <div className="space-y-0.5 font-mono text-xs text-neutral-600">
      {chart && (
        <div>
          <span className="text-neutral-400">chart </span>
          {chart.slice(0, 7)}
        </div>
      )}
      {valueShas.map((sha, i) => (
        <div key={i}>
          <span className="text-neutral-400">values </span>
          {sha}
        </div>
      ))}
    </div>
  );
}
