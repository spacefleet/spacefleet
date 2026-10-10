import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  History,
  Pencil,
  Play,
  RefreshCw,
  Server,
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
import { Breadcrumbs } from "../components/Breadcrumbs";
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

// How many of the newest runs the page lists; the full history is on the runs
// page.
const RECENT_RUNS = 5;

// ApplicationDetail is the per-app overview (route /applications/:appId). An
// application owns a deploy workflow (stages of components); this page shows an
// at-a-glance view of that workflow — colored by the latest run — with the run
// controls (deploy / preview / uninstall), its most recent runs, and the
// app's runner as a tag by its name. The workflow builder page is only for
// building/changing the workflow itself.
export function ApplicationDetail() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";

  const [app, setApp] = useState<Application | null>(null);
  useDocumentTitle(app?.name, "Applications");
  const [recentRuns, setRecentRuns] = useState<WorkflowRun[]>([]);
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

  // The newest workflow runs (newest first), for an at-a-glance history.
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}/runs", {
        params: { path: { id: appId } },
      });
      setRecentRuns(data?.runs?.slice(0, RECENT_RUNS) ?? []);
    })();
  }, [appId, currentOrg?.id]);

  // The runs are fetched once above; if the latest is still in flight, follow
  // its stream so its status stays current without polling (mirrors
  // WorkflowRunView). Only the latest can be in flight — an app runs one at a
  // time. The stream emits the full WorkflowRunDetail on each change.
  const latestRun = recentRuns[0] ?? null;
  const inFlight =
    latestRun != null && !TERMINAL.includes(latestRun.status);
  const { value: streamed } = useObjectStream<WorkflowRunDetail>(
    `/api/applications/${appId}/runs/${latestRun?.id}/stream`,
    inFlight,
  );

  // The latest run as shown: the fetched run, with the streamed fields folded
  // in when the stream is for that same run. Deriving it (rather than mutating
  // state in an effect) avoids racing the initial fetch and keeps the badge
  // current as the stream progresses.
  const displayRun: WorkflowRun | null =
    latestRun && streamed && streamed.id === latestRun.id
      ? { ...latestRun, ...streamed }
      : latestRun;
  const shownRuns = displayRun ? [displayRun, ...recentRuns.slice(1)] : [];

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
      <Breadcrumbs items={[{ label: "Applications", to: "/applications" }]} />

      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : error || !app ? (
        <p className="mt-6 text-sm text-red-400">{error ?? "Not found"}</p>
      ) : (
        <>
          <div className="mt-2 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <h1 className="break-all text-2xl font-bold tracking-tight">
                {app.name}
              </h1>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px]">
                {/* The runner: where the app's jobs run (deploy targets live
                    on the individual components). */}
                <span
                  title="Runner cluster: where this application's jobs run"
                  className="inline-flex items-center gap-1.5 border border-neutral-700 px-1.5 py-0.5 text-neutral-300"
                >
                  <Server className="h-3 w-3 text-neutral-500" />
                  <span className="text-neutral-500">runner</span>
                  {clusters[app.runner_cluster_id] ?? app.runner_cluster_id}
                </span>
                {app.imported && (
                  <span className="border border-neutral-700 px-1.5 py-0.5 uppercase tracking-wide text-neutral-400">
                    imported
                  </span>
                )}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => navigate(`/applications/${appId}/variables`)}
                className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
              >
                <Variable className="h-3.5 w-3.5" />
                Variables
              </button>
              {canEdit && (
                <>
                  <button
                    type="button"
                    onClick={() => navigate(`/applications/${appId}/edit`)}
                    className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
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
              builder page (the heading and Edit workflow both open it); a
              component card opens that component's page. Runs are started
              from here. */}
          <div className="mt-6 border border-neutral-800 bg-neutral-900">
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-neutral-800 px-4 py-2">
              <h2 className="text-[11px] font-medium uppercase tracking-wide">
                <Link
                  to={`/applications/${appId}/workflow`}
                  className="inline-flex items-center gap-2 text-neutral-500 hover:text-neutral-100"
                >
                  <Workflow className="h-3.5 w-3.5" />
                  Workflow
                </Link>
              </h2>
              <button
                type="button"
                onClick={() => navigate(`/applications/${appId}/workflow`)}
                title="Open the workflow editor"
                className="inline-flex items-center gap-1.5 border border-neutral-700 px-2.5 py-1 text-sm text-neutral-300 hover:bg-neutral-800"
              >
                <Pencil className="h-3.5 w-3.5" />
                Edit workflow
              </button>
            </div>
            <WorkflowStagesOverview
              appId={appId}
              clusterName={(id) => clusters[id]}
              latestStages={displayRun?.stages}
              onOpen={(componentId) =>
                navigate(`/applications/${appId}/workflow/nodes/${componentId}`)
              }
            />
            {canEdit && (
              <div className="flex flex-wrap items-center justify-end gap-2 border-t border-neutral-800 px-4 py-3">
                {runError && (
                  <p className="mr-auto text-sm text-red-400">{runError}</p>
                )}
                <button
                  type="button"
                  onClick={() => void startRun("drift")}
                  disabled={running}
                  aria-label="Refresh"
                  title="Refresh: check every OpenTofu component for changes made outside of OpenTofu (a read-only refresh-only plan)"
                  className="inline-flex items-center justify-center border border-neutral-700 p-2 text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
                >
                  <RefreshCw className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  onClick={() => void startRun("preview")}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
                >
                  Preview
                </button>
                <button
                  type="button"
                  onClick={() => setRunDialogOpen(true)}
                  disabled={running}
                  className="inline-flex items-center gap-1.5 bg-primary px-3 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
                >
                  <Play className="h-3.5 w-3.5" />
                  Run
                </button>
              </div>
            )}
          </div>

          {/* Recent runs (each links to its run view; the full history is
              on the runs page, filtered to this application) */}
          <div className="mt-6 border border-neutral-800 bg-neutral-900">
            <div className="flex items-center justify-between gap-2 border-b border-neutral-800 px-4 py-2">
              <div className="flex items-center gap-2">
                <History className="h-3.5 w-3.5 text-neutral-500" />
                <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
                  Recent runs
                </h2>
              </div>
              {shownRuns.length > 0 && (
                <Link
                  to={`/runs?application=${appId}`}
                  className="text-xs text-neutral-400 hover:text-neutral-100"
                >
                  View all runs
                </Link>
              )}
            </div>
            {shownRuns.length === 0 ? (
              <p className="px-4 py-6 text-sm text-neutral-400">
                No runs yet. Build the deploy workflow and start a run.
              </p>
            ) : (
              <ul aria-label="Recent runs" className="divide-y divide-neutral-800">
                {shownRuns.map((r) => (
                  <li key={r.id}>
                    <button
                      type="button"
                      onClick={() =>
                        navigate(`/applications/${appId}/runs/${r.id}`)
                      }
                      className="flex w-full items-center justify-between px-4 py-3 text-left text-sm hover:bg-neutral-800"
                    >
                      <span className="flex items-center gap-3">
                        <span className="capitalize text-neutral-300">
                          {runActionLabel(r.action, r.scope)}
                        </span>
                        <RunStatusBadge status={r.status} />
                        <StageBar stages={r.stages} />
                      </span>
                      <span className="text-neutral-400">
                        {new Date(r.created_at).toLocaleString()} ·{" "}
                        {formatDuration(
                          r.created_at,
                          r.finished_at ?? undefined,
                        )}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
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
