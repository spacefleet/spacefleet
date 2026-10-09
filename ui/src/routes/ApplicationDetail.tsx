import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  History,
  Pencil,
  Play,
  RefreshCw,
  Settings,
  Trash2,
  Variable,
  Workflow,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { useObjectStream } from "../lib/useObjectStream";
import type { components } from "../api/schema";
import { DeleteApplicationDialog } from "../components/DeleteApplicationDialog";
import { ActionsMenu } from "../components/ActionsMenu";
import { RunDialog } from "../components/RunDialog";
import { RunStatusBadge } from "../components/workflow/status";
import {
  runActionLabel,
  runStartError,
} from "../components/workflow/runAction";
import { WorkflowStagesOverview } from "../components/workflow/WorkflowStagesOverview";
import { StageBar } from "../components/workflow/StageBar";
import { formatDuration } from "../lib/duration";
import { useDocumentTitle } from "../lib/useDocumentTitle";

type Application = components["schemas"]["Application"];
type WorkflowRun = components["schemas"]["WorkflowRun"];
type WorkflowRunDetail = components["schemas"]["WorkflowRunDetail"];
type RunStatus = components["schemas"]["RunStatus"];

// A run is terminal once it settles; only then is the badge final and the stream
// closed. Mirrors WorkflowRunView's inFlight gating.
const TERMINAL: RunStatus[] = ["succeeded", "failed", "partial"];

// ApplicationDetail is the per-app overview (route /applications/:appId). An
// application owns a deploy workflow (stages of components); this page shows an
// at-a-glance view of that workflow — colored by the latest run — with the run
// controls (deploy / preview / uninstall), the app's runner, and the most
// recent run's status. The workflow builder page is only for building/changing
// the workflow itself.
export function ApplicationDetail() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [app, setApp] = useState<Application | null>(null);
  useDocumentTitle(app?.name, "Applications");
  const [latestRun, setLatestRun] = useState<WorkflowRun | null>(null);
  const [clusters, setClusters] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [runDialogOpen, setRunDialogOpen] = useState(false);

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
  // started from here (not the workflow builder) — the builder only edits the
  // workflow. A deploy goes through the Run dialog (its per-run options) and an
  // uninstall through the Delete dialog; preview and refresh start straight
  // from their buttons.
  const startRun = useCallback(
    async (action: "preview" | "drift") => {
      setRunning(true);
      setRunError(null);
      const { data, error, response } = await api.POST(
        "/api/applications/{id}/runs",
        { params: { path: { id: appId } }, body: { action } },
      );
      setRunning(false);
      if (error || !data) {
        setRunError(runStartError(error, response?.status));
        return;
      }
      navigate(`/applications/${appId}/runs/${data.id}`);
    },
    [appId, navigate],
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
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => navigate(`/applications/${appId}/variables`)}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
              >
                <Variable className="h-3.5 w-3.5" />
                Variables
              </button>
              {canEdit && (
                <>
                  <button
                    type="button"
                    onClick={() => navigate(`/applications/${appId}/edit`)}
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
                  >
                    <Settings className="h-3.5 w-3.5" />
                    Manage
                  </button>
                  <ActionsMenu
                    label={`${app.name} actions`}
                    items={[
                      {
                        label: "Delete",
                        icon: <Trash2 className="h-3.5 w-3.5" />,
                        danger: true,
                        onSelect: () => setDeleting(true),
                      },
                    ]}
                  />
                </>
              )}
            </div>
          </div>

          {/* Workflow: an at-a-glance view of the stages plus the run
              controls. Building/changing the workflow happens on the dedicated
              builder page; runs are started from here. */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-neutral-200 px-4 py-2">
              <div className="flex items-center gap-2">
                <Workflow className="h-3.5 w-3.5 text-neutral-400" />
                <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                  Workflow
                </h2>
              </div>
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
            <WorkflowStagesOverview
              appId={appId}
              clusterName={(id) => clusters[id]}
              latestStages={displayRun?.stages}
              onOpen={() => navigate(`/applications/${appId}/workflow`)}
            />
            {canEdit && (
              <div className="flex flex-wrap items-center justify-end gap-2 border-t border-neutral-200 px-4 py-3">
                {runError && (
                  <p className="mr-auto text-sm text-red-600">{runError}</p>
                )}
                <button
                  type="button"
                  onClick={() => void startRun("drift")}
                  disabled={running}
                  aria-label="Refresh"
                  title="Refresh: check every OpenTofu component for changes made outside of OpenTofu (a read-only refresh-only plan)"
                  className="inline-flex items-center justify-center border border-neutral-300 p-2 text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  <RefreshCw className="h-4 w-4" />
                </button>
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
                  onClick={() => setRunDialogOpen(true)}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                >
                  <Play className="h-3.5 w-3.5" />
                  Run
                </button>
              </div>
            )}
          </div>

          {/* Latest run (links to the run view; the full history is on the
              runs page, filtered to this application) */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex items-center justify-between gap-2 border-b border-neutral-200 px-4 py-2">
              <div className="flex items-center gap-2">
                <History className="h-3.5 w-3.5 text-neutral-400" />
                <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                  Latest run
                </h2>
              </div>
              {displayRun && (
                <Link
                  to={`/runs?application=${appId}`}
                  className="text-xs text-neutral-500 hover:text-neutral-900"
                >
                  View all runs
                </Link>
              )}
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
                    {runActionLabel(displayRun.action, displayRun.scope)}
                  </span>
                  <RunStatusBadge status={displayRun.status} />
                  <StageBar stages={displayRun.stages} />
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

          {runDialogOpen && (
            <RunDialog
              app={app}
              onClose={() => setRunDialogOpen(false)}
              onStarted={(runId) =>
                navigate(`/applications/${appId}/runs/${runId}`)
              }
            />
          )}
          {deleting && (
            <DeleteApplicationDialog
              app={app}
              onClose={() => setDeleting(false)}
              onDeleted={() => navigate("/applications")}
              onUninstallStarted={(runId) =>
                navigate(`/applications/${appId}/runs/${runId}`)
              }
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
