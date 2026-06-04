import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft, CheckCircle2, Loader2, XCircle } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { usePodLogs } from "../lib/usePodLogs";
import { formatDuration } from "../lib/duration";

type DeploymentDetail = components["schemas"]["DeploymentDetail"];
type Application = components["schemas"]["Application"];
type DeploymentStatus = components["schemas"]["DeploymentStatus"];

// DeploymentDetail is one rollout run's page (route
// /applications/:appId/deployments/:deploymentId): the action, status, timing,
// and the Helm output — like a CI run. Logs stream live while this run is the
// application's current, in-flight rollout; once it settles (or for any older
// run) the persisted logs captured at terminal are shown instead.
export function DeploymentDetail() {
  const { appId = "", deploymentId = "" } = useParams();
  const { currentOrg } = useOrg();
  const navigate = useNavigate();

  const [dep, setDep] = useState<DeploymentDetail | null>(null);
  const [app, setApp] = useState<Application | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [depRes, appRes] = await Promise.all([
      api.GET("/api/applications/{id}/deployments/{deploymentId}", {
        params: { path: { id: appId, deploymentId } },
      }),
      api.GET("/api/applications/{id}", { params: { path: { id: appId } } }),
    ]);
    if (depRes.error || !depRes.data) {
      setError(depRes.error?.message ?? "Could not load this deployment");
      setDep(null);
      setLoading(false);
      return;
    }
    setDep(depRes.data);
    setApp(appRes.data ?? null);
    setLoading(false);
  }, [appId, deploymentId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // This run streams live only while it is the app's current, in-flight rollout:
  // the live endpoints key off the application's current run, so an older run
  // (or this one once settled) reads its persisted logs instead.
  const isCurrentRun =
    !!dep && !!app && dep.run_name != null && dep.run_name === app.last_run_name;
  const live = isCurrentRun && dep?.status === "running";

  const {
    lines,
    status: logStatus,
    ended: logEnded,
    error: logError,
  } = usePodLogs(
    `/api/applications/${appId}/logs?run=${encodeURIComponent(dep?.run_name ?? "")}`,
    live,
  );

  // When the live stream ends (run settled, or the run went stale), reload so the
  // persisted logs + terminal status replace the live view. The reload flips this
  // run out of `live`, so the stream tears down and this fires only once.
  useEffect(() => {
    if (live && (logEnded || logError)) void load();
  }, [live, logEnded, logError, load]);

  const persisted = dep?.logs ?? "";
  const showLive = live && lines.length > 0;
  const logText = showLive
    ? lines.join("\n")
    : persisted || (live ? "Waiting for log output…" : "No logs were captured for this run.");

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate(`/applications/${appId}`)}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to application
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error || !dep ? (
        <p className="mt-6 text-sm text-red-600">{error ?? "Not found"}</p>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
                Deployment
              </p>
              <h1 className="mt-1 break-all text-2xl font-bold tracking-tight capitalize">
                {dep.action}
              </h1>
              <div className="mt-2">
                <DeploymentStatusBadge status={dep.status} />
              </div>
            </div>
          </div>

          {dep.message && (
            <p className="mt-3 text-sm text-neutral-500">{dep.message}</p>
          )}

          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <dl className="grid grid-cols-1 gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
              <Row label="Action" value={dep.action} />
              <Row label="Status" value={dep.status} />
              <Row label="Started" value={new Date(dep.created_at).toLocaleString()} />
              <Row
                label="Duration"
                value={formatDuration(dep.created_at, dep.finished_at)}
              />
              {dep.run_name && <Row label="Run" value={dep.run_name} />}
            </dl>
          </div>

          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex items-center justify-between border-b border-neutral-200 px-4 py-2">
              <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                Helm output
              </h2>
              {live && (
                <span className="text-xs text-neutral-500">live ({logStatus})</span>
              )}
            </div>
            <div className="px-4 py-3">
              <pre className="max-h-[32rem] overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                {logText}
              </pre>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="break-all capitalize text-neutral-800">{value || "—"}</dd>
    </div>
  );
}

// DeploymentStatusBadge renders a run's lifecycle (running/succeeded/failed),
// shared by the run page and the application's history list.
export function DeploymentStatusBadge({ status }: { status: DeploymentStatus }) {
  const styles: Record<DeploymentStatus, string> = {
    running: "bg-blue-100 text-blue-800",
    succeeded: "bg-green-100 text-green-800",
    failed: "bg-red-100 text-red-800",
  };
  const Icon: Record<DeploymentStatus, typeof CheckCircle2> = {
    running: Loader2,
    succeeded: CheckCircle2,
    failed: XCircle,
  };
  const I = Icon[status];
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[status]}`}
    >
      <I className={`h-3.5 w-3.5 ${status === "running" ? "animate-spin" : ""}`} />
      {status}
    </span>
  );
}
