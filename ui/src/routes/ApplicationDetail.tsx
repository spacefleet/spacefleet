import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft, History, Pencil, Trash2, Workflow } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { DeleteApplicationDialog } from "../components/DeleteApplicationDialog";
import { RunStatusBadge } from "../components/workflow/status";
import { formatDuration } from "../lib/duration";

type Application = components["schemas"]["Application"];
type WorkflowRun = components["schemas"]["WorkflowRun"];

// ApplicationDetail is the per-app overview (route /applications/:appId). An
// application owns a deploy workflow (a DAG of components); this page shows the
// app's targeting and a link into the workflow builder and run history, plus the
// most recent run's status. The deploy steps themselves live on the canvas.
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
              <button
                type="button"
                onClick={() => navigate(`/applications/${appId}/workflow`)}
                title="Open the deploy workflow canvas"
                className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800"
              >
                <Workflow className="h-3.5 w-3.5" />
                Workflow
              </button>
              <button
                type="button"
                onClick={() => navigate(`/applications/${appId}/runs`)}
                title="View this application's run history"
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
              >
                <History className="h-3.5 w-3.5" />
                Runs
              </button>
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

          {/* Targeting */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Targeting
            </h2>
            <dl className="mt-3 grid grid-cols-1 gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
              <Row
                label="Default target cluster"
                value={clusters[app.target_cluster_id] ?? app.target_cluster_id}
              />
              <Row label="Default target namespace" value={app.target_namespace} />
              <Row
                label="Runner cluster"
                value={clusters[app.runner_cluster_id] ?? app.runner_cluster_id}
              />
            </dl>
          </div>

          {/* Latest run (links to the run view; full history under Runs) */}
          <div className="mt-6 border border-neutral-200 bg-white">
            <div className="flex items-center gap-2 border-b border-neutral-200 px-4 py-2">
              <History className="h-3.5 w-3.5 text-neutral-400" />
              <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                Latest run
              </h2>
            </div>
            {!latestRun ? (
              <p className="px-4 py-6 text-sm text-neutral-500">
                No runs yet. Build the deploy workflow and start a run.
              </p>
            ) : (
              <button
                type="button"
                onClick={() =>
                  navigate(`/applications/${appId}/runs/${latestRun.id}`)
                }
                className="flex w-full items-center justify-between px-4 py-3 text-left text-sm hover:bg-neutral-50"
              >
                <span className="flex items-center gap-3">
                  <span className="capitalize text-neutral-700">
                    {latestRun.action}
                  </span>
                  <RunStatusBadge status={latestRun.status} />
                </span>
                <span className="text-neutral-500">
                  {new Date(latestRun.created_at).toLocaleString()} ·{" "}
                  {formatDuration(
                    latestRun.created_at,
                    latestRun.finished_at ?? undefined,
                  )}
                </span>
              </button>
            )}
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
