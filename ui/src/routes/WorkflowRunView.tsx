import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  type Edge,
  type Node,
  type ReactFlowInstance,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowLeft,
  Ban,
  Check,
  Maximize2,
  Minimize2,
  X,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { useObjectStream } from "../lib/useObjectStream";
import type { components } from "../api/schema";
import { formatDuration } from "../lib/duration";
import { DiffView } from "../components/DiffView";
import {
  RunNode,
  TypeBadge,
  type RunNodeData,
} from "../components/workflow/nodes";
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
// config carries the per-unit command ("plan"/"apply") an OpenTofu component's
// execution units were expanded with, which lets the view pair an apply step
// with its upstream plan step.
interface SnapshotNode {
  id: string;
  name: string;
  type: string;
  config?: Record<string, string>;
  depends_on?: string[];
}
interface GraphSnapshot {
  nodes?: SnapshotNode[];
}

type FlowNode = Node<RunNodeData & { componentRunId?: string }>;

// A run is terminal once it reaches a settled status; the stream closes then.
const TERMINAL: RunStatus[] = ["succeeded", "failed", "partial"];

// WorkflowRunView is the live DAG run view (route
// /applications/:appId/runs/:runId). It renders the run's snapshot graph as a
// read-only React Flow DAG, colors each node by its component-run status, and
// live-updates from the run SSE stream while in flight. Clicking a node shrinks
// the DAG to a zoomed-out strip and opens a full-width bottom panel with the
// step's logs (plus a diff for preview runs, and the upstream plan output for a
// tofu apply step), which can be expanded to fill the whole view — the DAG here
// is the picker; the logs are the content.
export function WorkflowRunView() {
  const { appId = "", runId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const location = useLocation();
  const canApprove = currentRole !== "viewer";

  // Where Back returns to. Pages that link here (the runs index) pass their own
  // location in router state; without it — arriving from the application page or
  // a deep link — Back goes to the application, matching where the journey
  // started rather than always dumping the user on the global run history.
  const from = (location.state as { from?: string } | null)?.from;
  const backTo = from ?? `/applications/${appId}`;
  const backLabel = from?.startsWith("/runs")
    ? "Back to runs"
    : "Back to application";

  const [run, setRun] = useState<WorkflowRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  // Whether the component-run panel fills the view (hiding the DAG) so logs get
  // the full window while following a deploy.
  const [expanded, setExpanded] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [flow, setFlow] = useState<ReactFlowInstance<FlowNode, Edge> | null>(
    null,
  );

  // Selecting a node shrinks the DAG to a strip above the logs panel; refit the
  // viewport after the container resizes (next frame) so the whole workflow
  // stays visible at the new size. The DAG is a picker here, not the focus.
  useEffect(() => {
    if (!flow) return;
    const frame = requestAnimationFrame(() => {
      void flow.fitView({ padding: 0.15 });
    });
    return () => cancelAnimationFrame(frame);
  }, [flow, selectedRunId, expanded]);

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
    const flowNodes: FlowNode[] = layoutNodes.map((n) => {
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

  // When the selected step is an OpenTofu apply unit, resolve its upstream plan
  // unit's component run so the panel can surface the plan output right where
  // the approve/reject decision is made (the parked apply step has no logs of
  // its own yet). Previews don't pair plan/apply (every unit dry-runs
  // independently), so they're excluded.
  const planSource = useMemo(() => {
    if (!selectedRunId || !run || run.action === "preview") return null;
    const cr = run.component_runs?.find((c) => c.id === selectedRunId);
    if (!cr?.component_id) return null;
    const snapNodes = parseSnapshot(run.graph)?.nodes ?? [];
    const node = snapNodes.find((n) => n.id === cr.component_id);
    if (node?.type !== "terraform" || node.config?.command !== "apply")
      return null;
    for (const depID of node.depends_on ?? []) {
      const dep = snapNodes.find((n) => n.id === depID);
      if (dep?.type === "terraform" && dep.config?.command === "plan") {
        const depRun = runsByComponent.get(depID);
        if (depRun) return { runId: depRun.id, name: dep.name };
      }
    }
    return null;
  }, [selectedRunId, run, runsByComponent]);

  return (
    <div className="flex h-[calc(100vh-7rem)] flex-col">
      <button
        type="button"
        onClick={() => navigate(backTo)}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        {backLabel}
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

          {/* The DAG on top, and — once a node is selected — a full-width bottom
              panel for that component run. The logs are what the user came for;
              the DAG is the picker. So selecting a node shrinks the DAG to a
              zoomed-out strip (the fitView effect above keeps the whole
              workflow in frame) and hands most of the view to the panel;
              expanding the panel hides the DAG entirely. */}
          <div className="flex min-h-0 flex-1 flex-col border border-neutral-200">
            {!(expanded && selectedRunId) && (
              <div
                className={`min-h-0 min-w-0 ${
                  selectedRunId ? "h-[30%] min-h-36 shrink-0" : "flex-1"
                }`}
              >
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  nodeTypes={nodeTypes}
                  nodesDraggable={false}
                  nodesConnectable={false}
                  proOptions={{ hideAttribution: true }}
                  onInit={setFlow}
                  // fitView won't zoom out past minZoom; a wide DAG in the
                  // shrunken strip needs more headroom than the 0.5 default.
                  minZoom={0.1}
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
            )}
            {selectedRunId && (
              <ComponentRunPanel
                key={selectedRunId}
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
                planRun={planSource}
                canApprove={canApprove}
                onDecided={load}
                expanded={expanded}
                onToggleExpanded={() => setExpanded((e) => !e)}
                onClose={() => {
                  setSelectedRunId(null);
                  setExpanded(false);
                }}
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}

// ComponentRunPanel is the full-width bottom panel for one component run: its
// logs (and for preview runs, its diff; for a tofu apply step, its upstream
// plan output) under a compact header, with the content area filling whatever
// height the panel has — the view minus the DAG strip by default, or all of it
// when expanded.
function ComponentRunPanel({
  appId,
  runId,
  componentRunId,
  liveStatus,
  isPreview,
  planRun,
  canApprove,
  onDecided,
  expanded,
  onToggleExpanded,
  onClose,
}: {
  appId: string;
  runId: string;
  componentRunId: string;
  // The live status from the run stream; when it changes (notably as the step
  // settles) the fetch effect re-runs so the detail (logs/diff) stays current.
  liveStatus?: ComponentRunStatus;
  isPreview: boolean;
  // The upstream tofu plan step backing this apply step, when there is one. Its
  // logs are the review material for the approval gate, so the panel surfaces
  // them on a "Plan output" tab — leading while the step is parked.
  planRun: { runId: string; name: string } | null;
  // Editor+ may approve/reject a step parked at awaiting_approval.
  canApprove: boolean;
  // Called after an approve/reject so the parent reloads the run detail; the SSE
  // stream also folds the resumed state in, which makes the buttons disappear.
  onDecided: () => void;
  expanded: boolean;
  onToggleExpanded: () => void;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<ComponentRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [planLogs, setPlanLogs] = useState<string | null>(null);
  const [deciding, setDeciding] = useState(false);
  const [decideError, setDecideError] = useState<string | null>(null);
  // For preview runs the diff is what the user came to inspect, so it leads; a
  // parked apply step opens on its plan output (the thing being approved);
  // everything else opens on logs. (The panel remounts per selection via key.)
  const [tab, setTab] = useState<"logs" | "diff" | "plan">(
    isPreview
      ? "diff"
      : planRun && liveStatus === "awaiting_approval"
        ? "plan"
        : "logs",
  );

  // The gate is live (open) only while the step is parked. Once the stream folds
  // a resumed status in, liveStatus changes and the buttons drop away.
  const awaitingApproval = liveStatus === "awaiting_approval";

  const decide = useCallback(
    async (decision: "approve" | "reject") => {
      setDeciding(true);
      setDecideError(null);
      const params = { path: { id: appId, runId, componentRunId } };
      const { error } =
        decision === "approve"
          ? await api.POST(
              "/api/applications/{id}/runs/{runId}/components/{componentRunId}/approve",
              { params },
            )
          : await api.POST(
              "/api/applications/{id}/runs/{runId}/components/{componentRunId}/reject",
              { params },
            );
      setDeciding(false);
      if (error) {
        setDecideError(error.message ?? "Could not record that decision");
        return;
      }
      onDecided();
    },
    [appId, runId, componentRunId, onDecided],
  );

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

  // Fetch the upstream plan step's logs for the Plan output tab. The plan step
  // already settled by the time its apply step is viewable, so its logs are
  // stable — keyed on the id, not the stream. Best-effort: a failure leaves the
  // tab on its "no output" placeholder.
  const planRunId = planRun?.runId;
  useEffect(() => {
    if (!planRunId) return;
    let cancelled = false;
    void (async () => {
      const { data } = await api.GET(
        "/api/applications/{id}/runs/{runId}/components/{componentRunId}",
        { params: { path: { id: appId, runId, componentRunId: planRunId } } },
      );
      if (!cancelled && data) setPlanLogs(data.logs ?? "");
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, runId, planRunId]);

  return (
    <section className="flex w-full min-h-0 flex-1 flex-col border-t border-neutral-200 bg-white">
      <div className="flex items-center justify-between gap-2 border-b border-neutral-200 px-4 py-2">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
          <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            Component run
          </p>
          <h2 className="truncate text-sm font-semibold text-neutral-900">
            {detail?.name ?? "…"}
          </h2>
          {/* The API types the component-run's type loosely (a string); it
              holds a ComponentType when present. */}
          {detail?.type && <TypeBadge type={detail.type as ComponentType} />}
          {detail && (
            <span className="text-xs capitalize text-neutral-500">
              {detail.status.replace(/_/g, " ")}
            </span>
          )}
          {detail?.approved_by && (
            <span className="text-xs text-neutral-500">
              decided by {detail.approved_by}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <button
            type="button"
            onClick={onToggleExpanded}
            aria-label={expanded ? "Collapse panel" : "Expand panel"}
            title={
              expanded
                ? "Shrink the panel and show the DAG again"
                : "Expand the panel to fill the view"
            }
            className="p-1 text-neutral-400 hover:text-neutral-900"
          >
            {expanded ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close panel"
            className="p-1 text-neutral-400 hover:text-neutral-900"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </div>

      {loading ? (
        <p className="px-4 py-4 text-sm text-neutral-500">Loading…</p>
      ) : error || !detail ? (
        <p className="px-4 py-4 text-sm text-red-600">{error ?? "Not found"}</p>
      ) : (
        <>
          {detail.message && (
            <p className="border-b border-neutral-100 px-4 py-2 text-sm text-neutral-600">
              {detail.message}
            </p>
          )}

          {/* Manual-approval gate. While the step is parked, an editor+ reviews
              the plan logs below and approves or rejects; the SSE stream folds
              the resumed state in, which clears awaitingApproval and hides
              these buttons. */}
          {awaitingApproval && (
            <div className="border-b border-violet-200 bg-violet-50 px-4 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <p className="text-sm font-medium text-violet-900">
                    Awaiting approval
                  </p>
                  <p className="mt-0.5 text-xs text-violet-800">
                    {planRun
                      ? "Review the plan output below, then approve to apply or reject to fail the run."
                      : "Approve to run this step, or reject to fail the run."}
                  </p>
                </div>
                {canApprove ? (
                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      onClick={() => void decide("approve")}
                      disabled={deciding}
                      className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
                    >
                      <Check className="h-3.5 w-3.5" />
                      Approve
                    </button>
                    <button
                      type="button"
                      onClick={() => void decide("reject")}
                      disabled={deciding}
                      className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
                    >
                      <X className="h-3.5 w-3.5" />
                      Reject
                    </button>
                  </div>
                ) : (
                  <p className="text-xs italic text-violet-700">
                    Only an editor or admin can approve this step.
                  </p>
                )}
              </div>
              {decideError && (
                <p className="mt-2 text-xs text-red-600">{decideError}</p>
              )}
            </div>
          )}

          {/* Preview runs get a Diff/Logs tab pair, a tofu apply step a
              Plan output/Logs pair, so each view spans the whole panel; other
              runs are logs-only with no tab chrome. */}
          {isPreview && (
            <div className="flex items-center gap-4 border-b border-neutral-200 px-4">
              <TabButton active={tab === "diff"} onClick={() => setTab("diff")}>
                Preview diff
                <ChangesBadge hasChanges={detail.has_changes} />
              </TabButton>
              <TabButton active={tab === "logs"} onClick={() => setTab("logs")}>
                Logs
              </TabButton>
            </div>
          )}
          {planRun && (
            <div className="flex items-center gap-4 border-b border-neutral-200 px-4">
              <TabButton active={tab === "plan"} onClick={() => setTab("plan")}>
                Plan output
              </TabButton>
              <TabButton active={tab === "logs"} onClick={() => setTab("logs")}>
                Logs
              </TabButton>
            </div>
          )}

          <div className="min-h-0 flex-1 p-3">
            {isPreview && tab === "diff" ? (
              detail.diff ? (
                <DiffView diff={detail.diff} className="h-full" />
              ) : (
                <p className="text-sm text-neutral-500">
                  {detail.has_changes === false
                    ? "No changes."
                    : "No diff captured."}
                </p>
              )
            ) : planRun && tab === "plan" ? (
              <pre className="h-full w-full overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                {planLogs === null
                  ? `Loading plan output from ${planRun.name}…`
                  : planLogs || "No plan output was captured."}
              </pre>
            ) : (
              <pre className="h-full w-full overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                {detail.logs || "No logs were captured for this step."}
              </pre>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-2 border-b-2 py-2 text-sm ${
        active
          ? "border-black font-medium text-neutral-900"
          : "border-transparent text-neutral-500 hover:text-neutral-900"
      }`}
    >
      {children}
    </button>
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
