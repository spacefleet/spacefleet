import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft, ArrowUpCircle, Play, Trash2 } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { AppStatusBadge } from "./Applications";
import { chartSourceLabel } from "../components/chartSources";
import { useObjectStream } from "../lib/useObjectStream";
import { usePodLogs } from "../lib/usePodLogs";

type Application = components["schemas"]["Application"];
type TektonRun = components["schemas"]["TektonRun"];

// Statuses where a rollout job is in flight — the status/run/log streams are
// open and the UI shows live progress.
const IN_FLIGHT: Application["status"][] = ["deploying", "uninstalling"];

// ApplicationDetail is the single place to see and manage one application
// (route /applications/:appId). It shows the chart config and current rollout
// status, offers Deploy/Upgrade/Uninstall, and streams the live rollout status,
// the TaskRun, and the helm logs while a rollout runs. The status reflects the
// outcome of the last rollout (helm --wait), not ongoing release health.
export function ApplicationDetail() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [app, setApp] = useState<Application | null>(null);
  const [clusters, setClusters] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
  const hasRun = !!app?.last_run_name;

  // Follow the rollout status live while a job is in flight; on settle, the
  // stream delivers the terminal row, which we fold in directly.
  const { value: streamedApp } = useObjectStream<Application>(
    `/api/applications/${appId}/stream`,
    inFlight,
  );
  useEffect(() => {
    if (streamedApp) setApp(streamedApp);
  }, [streamedApp]);

  // Follow the rollout TaskRun while in flight (its phase/message complement the
  // app status with the run-level detail).
  const { value: run } = useObjectStream<TektonRun>(
    `/api/applications/${appId}/run/stream`,
    inFlight && hasRun,
  );

  // Follow the helm logs while there's a run to read (the stream ends on eof).
  const { lines, status: logStatus } = usePodLogs(
    `/api/applications/${appId}/logs`,
    hasRun,
  );

  async function rollout(action: "deploy" | "upgrade") {
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST("/api/applications/{id}/rollout", {
      params: { path: { id: appId } },
      body: { action },
    });
    setBusy(false);
    if (error) setActionError(error.message ?? "Could not start the rollout");
    else if (data) setApp(data);
  }

  async function uninstall() {
    if (!confirm("Uninstall this application's Helm release?")) return;
    setBusy(true);
    setActionError(null);
    const { data, error } = await api.POST("/api/applications/{id}/uninstall", {
      params: { path: { id: appId } },
    });
    setBusy(false);
    if (error) setActionError(error.message ?? "Could not start the uninstall");
    else if (data) setApp(data);
  }

  async function onDelete() {
    if (!confirm("Delete this application? This does not uninstall the release."))
      return;
    setBusy(true);
    const { error } = await api.DELETE("/api/applications/{id}", {
      params: { path: { id: appId } },
    });
    if (error) {
      setActionError(error.message ?? "Could not delete this application");
      setBusy(false);
      return;
    }
    navigate("/applications");
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
              <div className="mt-2">
                <AppStatusBadge status={app.status} message={app.status_message} />
              </div>
            </div>
            {canEdit && (
              <div className="flex flex-wrap items-center justify-end gap-2">
                {app.status === "deployed" ? (
                  <button
                    type="button"
                    onClick={() => void rollout("upgrade")}
                    disabled={busy || inFlight}
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                  >
                    <ArrowUpCircle className="h-3.5 w-3.5" />
                    Upgrade
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => void rollout("deploy")}
                    disabled={busy || inFlight}
                    className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                  >
                    <Play className="h-3.5 w-3.5" />
                    Deploy
                  </button>
                )}
                {(app.status === "deployed" || app.status === "failed") && (
                  <button
                    type="button"
                    onClick={() => void uninstall()}
                    disabled={busy || inFlight}
                    className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                  >
                    Uninstall
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => void onDelete()}
                  disabled={busy || inFlight}
                  title="Delete this application"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
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

          {/* Live rollout */}
          {hasRun && (
            <div className="mt-6 border border-neutral-200 bg-white">
              <div className="flex items-center justify-between border-b border-neutral-200 px-4 py-2">
                <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                  Last rollout
                </h2>
                {run && (
                  <span className="text-xs text-neutral-500">
                    run {run.phase.toLowerCase()}
                  </span>
                )}
              </div>
              <div className="px-4 py-3">
                <p className="mb-2 text-xs text-neutral-500">
                  Helm output ({logStatus})
                </p>
                <pre className="max-h-96 overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                  {lines.length === 0
                    ? "Waiting for log output…"
                    : lines.join("\n")}
                </pre>
              </div>
            </div>
          )}
        </>
      )}
    </div>
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
