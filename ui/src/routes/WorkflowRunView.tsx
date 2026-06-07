import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  type Edge,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ArrowLeft, Ban, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { useObjectStream } from "../lib/useObjectStream";
import type { components } from "../api/schema";
import { formatDuration } from "../lib/duration";
import { DiffView } from "../components/DiffView";
import { RunNode, type RunNodeData } from "../components/workflow/nodes";
import { RunStatusBadge } from "../components/workflow/status";

type WorkflowRunDetail = components["schemas"]["WorkflowRunDetail"];
type ComponentRun = components["schemas"]["ComponentRun"];
type ComponentRunDetail = components["schemas"]["ComponentRunDetail"];
type ComponentType = components["schemas"]["ComponentType"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];
type RunStatus = components["schemas"]["RunStatus"];

const nodeTypes = { run: RunNode };

// GraphSnapshot mirrors the backend's lib/workflows GraphSnapshot JSON written
// to WorkflowRun.graph: nodes with their as-run config + depends_on (edges).
interface SnapshotNode {
  id: string;
  name: string;
  type: string;
  depends_on?: string[];
}
interface GraphSnapshot {
  nodes?: SnapshotNode[];
}

// A run is terminal once it reaches a settled status; the stream closes then.
const TERMINAL: RunStatus[] = ["succeeded", "failed", "partial"];

// WorkflowRunView is the live DAG run view (route
// /applications/:appId/runs/:runId). It renders the run's snapshot graph as a
// read-only React Flow DAG, colors each node by its component-run status, and
// live-updates from the run SSE stream while in flight. Clicking a node fetches
// its detail (logs, and for preview runs a diff).
export function WorkflowRunView() {
  const { appId = "", runId = "" } = useParams();
  const { currentOrg } = useOrg();
  const navigate = useNavigate();

  const [run, setRun] = useState<WorkflowRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET(
      "/api/applications/{id}/runs/{runId}",
      { params: { path: { id: appId, runId } } },
    );
    if (error || !data) {
      setError(error?.message ?? "Could not load this run");
      setLoading(false);
      return;
    }
    setRun(data);
    setLoading(false);
  }, [appId, runId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Follow the run live until it settles. The stream emits the full
  // WorkflowRunDetail on each change (a `snapshot` event), so we fold the latest
  // straight into state.
  const inFlight = run != null && !TERMINAL.includes(run.status);
  const { value: streamed } = useObjectStream<WorkflowRunDetail>(
    `/api/applications/${appId}/runs/${runId}/stream`,
    inFlight,
  );
  useEffect(() => {
    if (streamed) setRun(streamed);
  }, [streamed]);

  // Cancel an in-flight run: marks it failed server-side. The stream then folds
  // the terminal state in (or the reaper would have, eventually) — here we also
  // fold the returned run so the view settles immediately.
  const cancel = useCallback(async () => {
    setCancelling(true);
    setCancelError(null);
    const { data, error } = await api.POST(
      "/api/applications/{id}/runs/{runId}/cancel",
      { params: { path: { id: appId, runId } } },
    );
    setCancelling(false);
    if (error || !data) {
      setCancelError(error?.message ?? "Could not cancel this run");
      return;
    }
    // Re-load the full detail so the component-run rows reflect the cancellation.
    void load();
  }, [appId, runId, load]);

  // Component runs keyed by their source component id, so a snapshot node can
  // find its run status. Falls back to matching by id directly.
  const runsByComponent = useMemo(() => {
    const m = new Map<string, ComponentRun>();
    for (const cr of run?.component_runs ?? []) {
      if (cr.component_id) m.set(cr.component_id, cr);
    }
    return m;
  }, [run]);

  // Parse the snapshot graph for layout; fall back to deriving nodes from the
  // component runs themselves if the snapshot is missing/unparseable.
  const { nodes, edges } = useMemo(() => {
    const snapshot = parseSnapshot(run?.graph);
    const snapNodes = snapshot?.nodes ?? [];

    let layoutNodes: { id: string; name: string; type: string; depends_on: string[] }[];
    if (snapNodes.length > 0) {
      layoutNodes = snapNodes.map((n) => ({
        id: n.id,
        name: n.name,
        type: n.type,
        depends_on: n.depends_on ?? [],
      }));
    } else {
      // Derive from component runs (no edges available without the snapshot).
      layoutNodes = (run?.component_runs ?? []).map((cr) => ({
        id: cr.component_id ?? cr.id,
        name: cr.name ?? "(unnamed)",
        type: cr.type ?? "helm",
        depends_on: [],
      }));
    }

    const depthOf = computeDepths(layoutNodes);
    const perDepth: Record<number, number> = {};
    const flowNodes: Node<RunNodeData & { componentRunId?: string }>[] =
      layoutNodes.map((n) => {
        const cr = runsByComponent.get(n.id);
        const depth = depthOf[n.id] ?? 0;
        const col = perDepth[depth] ?? 0;
        perDepth[depth] = col + 1;
        return {
          id: n.id,
          type: "run",
          position: { x: 80 + col * 240, y: 60 + depth * 150 },
          data: {
            name: n.name,
            type: (n.type as ComponentType) ?? "helm",
            status: cr?.status ?? "pending",
            componentRunId: cr?.id,
          },
        };
      });

    const flowEdges: Edge[] = [];
    for (const n of layoutNodes) {
      for (const dep of n.depends_on) {
        flowEdges.push({ id: `${dep}->${n.id}`, source: dep, target: n.id });
      }
    }
    return { nodes: flowNodes, edges: flowEdges };
  }, [run, runsByComponent]);

  return (
    <div className="flex h-[calc(100vh-7rem)] flex-col">
      <button
        type="button"
        onClick={() => navigate(`/applications/${appId}/runs`)}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to runs
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error || !run ? (
        <p className="mt-6 text-sm text-red-600">{error ?? "Not found"}</p>
      ) : (
        <>
          <div className="mt-3 flex flex-wrap items-center justify-between gap-3 pb-3">
            <div>
              <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
                Workflow run
              </p>
              <h1 className="mt-0.5 text-xl font-bold capitalize tracking-tight">
                {run.action}
              </h1>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              {inFlight && (
                <button
                  type="button"
                  onClick={() => void cancel()}
                  disabled={cancelling}
                  title="Cancel this run"
                  className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                >
                  <Ban className="h-3.5 w-3.5" />
                  {cancelling ? "Cancelling…" : "Cancel run"}
                </button>
              )}
              <RunStatusBadge status={run.status} />
              <span className="text-xs text-neutral-500">
                started {new Date(run.created_at).toLocaleString()} ·{" "}
                {formatDuration(run.created_at, run.finished_at ?? undefined)}
              </span>
            </div>
          </div>

          {run.message && (
            <p className="pb-2 text-sm text-neutral-500">{run.message}</p>
          )}
          {cancelError && (
            <p className="pb-2 text-sm text-red-600">{cancelError}</p>
          )}

          <div className="flex min-h-0 flex-1 border border-neutral-200">
            <div className="min-w-0 flex-1">
              <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                nodesDraggable={false}
                nodesConnectable={false}
                onNodeClick={(_, n) => {
                  const crId = (n.data as { componentRunId?: string }).componentRunId;
                  if (crId) setSelectedRunId(crId);
                }}
                fitView
              >
                <Background />
                <Controls showInteractive={false} />
              </ReactFlow>
            </div>
            {selectedRunId && (
              <ComponentRunPanel
                appId={appId}
                runId={runId}
                componentRunId={selectedRunId}
                // The live status of the selected component run, folded from the
                // run stream. Passing it as a key into the panel's fetch effect
                // forces a re-fetch when the step transitions (e.g. to a terminal
                // state), so logs/diff populate without a manual close/reopen.
                liveStatus={
                  run.component_runs?.find((cr) => cr.id === selectedRunId)
                    ?.status
                }
                isPreview={run.action === "preview"}
                onClose={() => setSelectedRunId(null)}
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}

// ComponentRunPanel fetches and shows one component run's detail: its logs (and
// for preview runs, its diff via DiffView with a changes indicator).
function ComponentRunPanel({
  appId,
  runId,
  componentRunId,
  liveStatus,
  isPreview,
  onClose,
}: {
  appId: string;
  runId: string;
  componentRunId: string;
  // The live status from the run stream; when it changes (notably as the step
  // settles) the fetch effect re-runs so the detail (logs/diff) stays current.
  liveStatus?: ComponentRunStatus;
  isPreview: boolean;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<ComponentRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setDetail(null);
    void (async () => {
      const { data, error } = await api.GET(
        "/api/applications/{id}/runs/{runId}/components/{componentRunId}",
        { params: { path: { id: appId, runId, componentRunId } } },
      );
      if (cancelled) return;
      if (error || !data) {
        setError(error?.message ?? "Could not load this component run");
      } else {
        setDetail(data);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, runId, componentRunId, liveStatus]);

  return (
    <aside className="flex h-full w-[28rem] shrink-0 flex-col overflow-y-auto border-l border-neutral-200 bg-white">
      <div className="flex items-start justify-between gap-2 border-b border-neutral-200 px-4 py-3">
        <div className="min-w-0">
          <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            Component run
          </p>
          <h2 className="mt-0.5 truncate text-sm font-semibold text-neutral-900">
            {detail?.name ?? "…"}
          </h2>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close panel"
          className="p-1 text-neutral-400 hover:text-neutral-900"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="flex-1 space-y-4 px-4 py-4">
        {loading ? (
          <p className="text-sm text-neutral-500">Loading…</p>
        ) : error || !detail ? (
          <p className="text-sm text-red-600">{error ?? "Not found"}</p>
        ) : (
          <>
            <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
              <Row label="Status" value={detail.status} />
              <Row label="Type" value={detail.type ?? "—"} />
              {detail.message && <Row label="Message" value={detail.message} wide />}
            </dl>

            {isPreview && (
              <div>
                <div className="mb-1 flex items-center gap-2">
                  <h3 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                    Preview diff
                  </h3>
                  <ChangesBadge hasChanges={detail.has_changes} />
                </div>
                {detail.diff ? (
                  <DiffView diff={detail.diff} />
                ) : (
                  <p className="text-sm text-neutral-500">
                    {detail.has_changes === false
                      ? "No changes."
                      : "No diff captured."}
                  </p>
                )}
              </div>
            )}

            <div>
              <h3 className="mb-1 text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                Logs
              </h3>
              <pre className="max-h-[28rem] overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                {detail.logs || "No logs were captured for this step."}
              </pre>
            </div>
          </>
        )}
      </div>
    </aside>
  );
}

function ChangesBadge({ hasChanges }: { hasChanges?: boolean }) {
  if (hasChanges === undefined) return null;
  return hasChanges ? (
    <span className="inline-flex items-center px-2 py-0.5 text-xs font-medium bg-amber-100 text-amber-800">
      changes
    </span>
  ) : (
    <span className="inline-flex items-center px-2 py-0.5 text-xs font-medium bg-neutral-100 text-neutral-600">
      no changes
    </span>
  );
}

function Row({ label, value, wide }: { label: string; value: string; wide?: boolean }) {
  return (
    <div className={`flex flex-col ${wide ? "col-span-2" : ""}`}>
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="break-words capitalize text-neutral-800">{value || "—"}</dd>
    </div>
  );
}

// parseSnapshot defensively parses the run's graph JSON string.
function parseSnapshot(raw: string | undefined): GraphSnapshot | null {
  if (!raw || raw.trim() === "") return null;
  try {
    return JSON.parse(raw) as GraphSnapshot;
  } catch {
    return null;
  }
}

// computeDepths assigns each node a DAG depth (longest path from a root) for a
// simple top-down layered layout. Cycles can't occur (the server validated
// acyclic), but the visited guard keeps it safe regardless.
function computeDepths(
  nodes: { id: string; depends_on: string[] }[],
): Record<string, number> {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const depth: Record<string, number> = {};
  const resolve = (id: string, seen: Set<string>): number => {
    if (depth[id] !== undefined) return depth[id];
    if (seen.has(id)) return 0;
    seen.add(id);
    const n = byId.get(id);
    if (!n || n.depends_on.length === 0) {
      depth[id] = 0;
      return 0;
    }
    const d = 1 + Math.max(...n.depends_on.map((p) => resolve(p, seen)));
    depth[id] = d;
    return d;
  };
  for (const n of nodes) resolve(n.id, new Set());
  return depth;
}
