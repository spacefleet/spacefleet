import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  History,
  Pencil,
  Play,
  Trash2,
  Workflow,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { useObjectStream } from "../lib/useObjectStream";
import type { components } from "../api/schema";
import { DeleteApplicationDialog } from "../components/DeleteApplicationDialog";
import { RunStatusBadge } from "../components/workflow/status";
import { WorkflowOverview } from "../components/workflow/WorkflowOverview";
import { VariablesEditor } from "../components/VariablesEditor";
import { formatDuration } from "../lib/duration";

type Application = components["schemas"]["Application"];
type WorkflowRun = components["schemas"]["WorkflowRun"];
type WorkflowRunDetail = components["schemas"]["WorkflowRunDetail"];
type RunStatus = components["schemas"]["RunStatus"];
type RunAction = components["schemas"]["RunAction"];

// A run is terminal once it settles; only then is the badge final and the stream
// closed. Mirrors WorkflowRunView's inFlight gating.
const TERMINAL: RunStatus[] = ["succeeded", "failed", "partial"];

// ApplicationDetail is the per-app overview (route /applications/:appId). An
// application owns a deploy workflow (a DAG of components); this page shows an
// at-a-glance view of that workflow with the run controls (deploy / preview /
// uninstall), the app's targeting, and the most recent run's status. The
// workflow editor page is only for building/changing the DAG itself.
export function ApplicationDetail() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [app, setApp] = useState<Application | null>(null);
  const [latestRun, setLatestRun] = useState<WorkflowRun | null>(null);
  const [clusters, setClusters] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [forceRoll, setForceRoll] = useState(false);

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

  // The most recent workflow run (for an at-a-glance status), if any.
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}/runs", {
        params: { path: { id: appId } },
      });
      setLatestRun(data?.runs?.[0] ?? null);
    })();
  }, [appId, currentOrg?.id]);

  // The badge is fetched once above; if that run is still in flight, follow its
  // stream so the status stays current without polling (mirrors WorkflowRunView).
  // The stream emits the full WorkflowRunDetail on each change.
  const inFlight =
    latestRun != null && !TERMINAL.includes(latestRun.status);
  const { value: streamed } = useObjectStream<WorkflowRunDetail>(
    `/api/applications/${appId}/runs/${latestRun?.id}/stream`,
    inFlight,
  );

  // The run shown by the badge: the fetched run, with the streamed fields folded
  // in when the stream is for that same run. Deriving it (rather than mutating
  // state in an effect) avoids racing the initial fetch and keeps the badge
  // current as the stream progresses.
  const displayRun: WorkflowRun | null =
    latestRun && streamed && streamed.id === latestRun.id
      ? { ...latestRun, ...streamed }
      : latestRun;

  // Start a run against the saved workflow and jump to its live view. Runs are
  // started from here (not the workflow editor) — the editor only builds the DAG.
  const startRun = useCallback(
    async (action: RunAction) => {
      setRunning(true);
      setRunError(null);
      const body =
        action === "deploy" ? { action, force: forceRoll } : { action };
      const { data, error, response } = await api.POST(
        "/api/applications/{id}/runs",
        { params: { path: { id: appId } }, body },
      );
      setRunning(false);
      if (error || !data) {
        if (response?.status === 409) {
          setRunError("A run is already in progress for this application.");
        } else {
          setRunError(error?.message ?? "Could not start the run");
        }
        return;
      }
      navigate(`/applications/${appId}/runs/${data.id}`);
    },
    [appId, forceRoll, navigate],
  );

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
              {app.imported && (
                <span className="mt-2 inline-block border border-neutral-300 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-neutral-500">
                  imported
                </span>
              )}
            </div>
            <div className="flex flex-wrap items-center justify-end gap-2">
              {canEdit && (
                <>
                  <button
                    type="button"
                    onClick={() => navigate(`/applications/${appId}/edit`)}
                    title="Edit this application"
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
                  >
                    <Pencil className="h-3.5 w-3.5" />
                    Edit
                  </button>
                  <button
                    type="button"
                    onClick={() => setDeleting(true)}
                    title="Delete this application"
                    className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                    Delete
                  </button>
                </>
              )}
            </div>
          </div>

          {/* Workflow: an at-a-glance view of the deploy DAG plus the run
              controls. Building/changing the DAG happens on the dedicated
              workflow editor page; runs are started from here. */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-neutral-200 px-4 py-2">
              <div className="flex items-center gap-2">
                <Workflow className="h-3.5 w-3.5 text-neutral-400" />
                <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                  Workflow
                </h2>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={() => navigate(`/runs?application=${appId}`)}
                  title="View this application's run history"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-2.5 py-1 text-sm text-neutral-700 hover:bg-neutral-50"
                >
                  <History className="h-3.5 w-3.5" />
                  Run history
                </button>
                <button
                  type="button"
                  onClick={() => navigate(`/applications/${appId}/workflow`)}
                  title="Open the workflow editor"
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-2.5 py-1 text-sm text-neutral-700 hover:bg-neutral-50"
                >
                  <Pencil className="h-3.5 w-3.5" />
                  Edit workflow
                </button>
              </div>
            </div>
            <WorkflowOverview appId={appId} />
            {canEdit && (
              <div className="flex flex-wrap items-center justify-end gap-2 border-t border-neutral-200 px-4 py-3">
                {runError && (
                  <p className="mr-auto text-sm text-red-600">{runError}</p>
                )}
                <button
                  type="button"
                  onClick={() => void startRun("preview")}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  Preview
                </button>
                <button
                  type="button"
                  onClick={() => void startRun("uninstall")}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  Uninstall
                </button>
                <label
                  title="Run the deploy's Helm step as a forced upgrade so pods roll even when the rendered manifests are unchanged"
                  className="inline-flex items-center gap-1.5 text-sm text-neutral-600"
                >
                  <input
                    type="checkbox"
                    checked={forceRoll}
                    onChange={(e) => setForceRoll(e.target.checked)}
                    className="h-3.5 w-3.5 accent-black"
                  />
                  Force workload roll
                </label>
                <button
                  type="button"
                  onClick={() => void startRun("deploy")}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                >
                  <Play className="h-3.5 w-3.5" />
                  Deploy
                </button>
              </div>
            )}
          </div>

          {/* Latest run (links to the run view; full history under Run history) */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex items-center gap-2 border-b border-neutral-200 px-4 py-2">
              <History className="h-3.5 w-3.5 text-neutral-400" />
              <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                Latest run
              </h2>
            </div>
            {!displayRun ? (
              <p className="px-4 py-6 text-sm text-neutral-500">
                No runs yet. Build the deploy workflow and start a run.
              </p>
            ) : (
              <button
                type="button"
                onClick={() =>
                  navigate(`/applications/${appId}/runs/${displayRun.id}`)
                }
                className="flex w-full items-center justify-between px-4 py-3 text-left text-sm hover:bg-neutral-50"
              >
                <span className="flex items-center gap-3">
                  <span className="capitalize text-neutral-700">
                    {displayRun.action}
                  </span>
                  <RunStatusBadge status={displayRun.status} />
                </span>
                <span className="text-neutral-500">
                  {new Date(displayRun.created_at).toLocaleString()} ·{" "}
                  {formatDuration(
                    displayRun.created_at,
                    displayRun.finished_at ?? undefined,
                  )}
                </span>
              </button>
            )}
          </div>

          {/* Runner (deploy targets live on the individual components) */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Runner
            </h2>
            <dl className="mt-3 grid grid-cols-1 gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
              <Row
                label="Runner cluster"
                value={clusters[app.runner_cluster_id] ?? app.runner_cluster_id}
              />
            </dl>
          </div>

          {/* Variables (app-level: passed to every component job as env vars) */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Variables
            </h2>
            <p className="mb-3 mt-1 text-xs text-neutral-500">
              Passed to every component job in this application as environment
              variables. A component can override one of these for its own job.
              A sensitive value is sealed and never shown again.
            </p>
            <VariablesEditor scope={{ kind: "app", appId }} canEdit={canEdit} />
          </div>

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

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="break-all text-neutral-800">{value || "—"}</dd>
    </div>
  );
}
