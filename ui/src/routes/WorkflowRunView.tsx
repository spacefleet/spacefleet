import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import {
  ArrowLeft,
  Ban,
  Check,
  Maximize2,
  Minimize2,
  Trash2,
  X,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { useObjectStream } from "../lib/useObjectStream";
import { useApplicationName } from "../lib/useApplicationName";
import { useDocumentTitle } from "../lib/useDocumentTitle";
import { usePodLogs } from "../lib/usePodLogs";
import type { components } from "../api/schema";
import { formatDuration } from "../lib/duration";
import { DiffView } from "../components/DiffView";
import { DeleteApplicationDialog } from "../components/DeleteApplicationDialog";
import { TypeBadge } from "../components/workflow/TypeBadge";
import {
  ComponentStatusIcon,
  RunStatusBadge,
} from "../components/workflow/status";
import {
  runActionLabel,
  runScopeDescription,
  runTriggerDescription,
  stateOpDescription,
} from "../components/workflow/runAction";
import {
  PlanCounts,
  PlanResourceList,
  PlanSummaryBar,
} from "../components/workflow/PlanView";
import { OutputsTable } from "../components/workflow/OutputsTable";
import { ResourcesTable } from "../components/workflow/ResourcesTable";

type WorkflowRunDetail = components["schemas"]["WorkflowRunDetail"];
type PlanSummary = components["schemas"]["PlanSummary"];
type ComponentRun = components["schemas"]["ComponentRun"];
type ComponentRunDetail = components["schemas"]["ComponentRunDetail"];
type ComponentType = components["schemas"]["ComponentType"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];
type RunStatus = components["schemas"]["RunStatus"];
type PolicyVerdict = components["schemas"]["PolicyVerdict"];
type RunStage = components["schemas"]["RunStage"];
type RunStageComponent = components["schemas"]["RunStageComponent"];

// GraphSnapshot mirrors the backend's lib/workflows GraphSnapshot JSON written
// to WorkflowRun.graph: the execution steps with their as-run config and the
// steps each waits on. config carries the per-step command ("plan"/"apply") an
// OpenTofu component was expanded into, which lets the view pair an apply step
// with its plan step.
interface SnapshotNode {
  id: string;
  name: string;
  type: string;
  config?: Record<string, string>;
  depends_on?: string[];
  approval_policy?: ApprovalPolicy;
}
type ApprovalPolicy = components["schemas"]["ApprovalPolicy"];
interface GraphSnapshot {
  nodes?: SnapshotNode[];
}

// A run is terminal once it reaches a settled status; the stream closes then.
const TERMINAL: RunStatus[] = ["succeeded", "failed", "partial"];

// A read-only run (preview, drift check) applies nothing: every step is an
// independent dry-run/plan, there are no apply units to pair with plan units,
// and the panel leads with what the step found rather than its logs.
function isReadOnlyAction(action: string): boolean {
  return action === "preview" || action === "drift";
}

type StateOperation = components["schemas"]["StateOperation"];

// WorkflowRunView is the live run view (route /applications/:appId/runs/:runId).
// A rail on the left lists the run's stages and their components with live
// statuses — an OpenTofu component with its plan and apply steps under it — and
// the main pane shows the selected step: its logs, plus a diff for preview
// runs, the plan behind a tofu apply step, outputs, resources, and the approval
// gate. The selected step is the ?step= search param, so a step is linkable;
// without one the view follows the step that most needs attention as the run
// progresses. Expanding the pane hides the rail.
export function WorkflowRunView() {
  const { appId = "", runId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
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
  // Whether the step pane fills the view (hiding the rail) so logs get the full
  // window while following a deploy.
  const [expanded, setExpanded] = useState(false);
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

  // The window title leads with the run's action and live status, so a run
  // left in a background tab shows its progress.
  const appName = useApplicationName(appId);
  const actionLabel = run ? runActionLabel(run.action, run.scope) : "";
  useDocumentTitle(
    run &&
      `${actionLabel.charAt(0).toUpperCase()}${actionLabel.slice(1)} (${run.status.replace(/_/g, " ")})`,
    appName,
  );

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

  // Component runs (steps) by their own id, for the rail and the selection.
  const stepById = useMemo(() => {
    const m = new Map<string, ComponentRun>();
    for (const cr of run?.component_runs ?? []) m.set(cr.id, cr);
    return m;
  }, [run]);

  // Component runs keyed by their snapshot node id, so a snapshot node can find
  // its step (the apply step's plan pairing below).
  const runsByComponent = useMemo(() => {
    const m = new Map<string, ComponentRun>();
    for (const cr of run?.component_runs ?? []) {
      if (cr.component_id) m.set(cr.component_id, cr);
    }
    return m;
  }, [run]);

  const stages = useMemo(() => (run ? runStagesOf(run) : []), [run]);

  // The selected step: the ?step= param when it names one of this run's steps,
  // else the one most worth looking at right now (which moves as the run
  // progresses, until the user picks one).
  const stepParam = searchParams.get("step");
  const selectedRunId = useMemo(() => {
    if (stepParam && stepById.has(stepParam)) return stepParam;
    const ordered = stages
      .flatMap((st) => st.components.flatMap((c) => c.component_run_ids))
      .map((id) => stepById.get(id))
      .filter((s): s is ComponentRun => s != null);
    return mostRelevantStep(ordered)?.id ?? null;
  }, [stepParam, stepById, stages]);

  // Selecting a step records it in the URL (replacing, so stepping through a
  // run doesn't fill the history) and keeps the router state that carries
  // Back's target.
  const selectStep = useCallback(
    (id: string) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.set("step", id);
          return next;
        },
        { replace: true, state: location.state },
      );
    },
    [setSearchParams, location.state],
  );

  // When the selected step is an OpenTofu apply unit, resolve its upstream plan
  // unit's component run so the panel can surface the plan output right where
  // the approve/reject decision is made (the parked apply step has no logs of
  // its own yet). Previews don't pair plan/apply (every unit dry-runs
  // independently), so they're excluded.
  const planSource = useMemo(() => {
    if (!selectedRunId || !run || isReadOnlyAction(run.action)) return null;
    const cr = stepById.get(selectedRunId);
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
  }, [selectedRunId, run, stepById, runsByComponent]);

  // The selected step's approval policy, as snapshotted at run start, so the
  // gate can say who may approve and how many approvals it needs.
  const selectedPolicy = useMemo(() => {
    if (!selectedRunId || !run) return null;
    const cr = stepById.get(selectedRunId);
    if (!cr?.component_id) return null;
    const node = parseSnapshot(run.graph)?.nodes?.find(
      (n) => n.id === cr.component_id,
    );
    return node?.approval_policy ?? null;
  }, [selectedRunId, run, stepById]);

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
                {runActionLabel(run.action, run.scope)}
              </h1>
              {run.scope && (
                <p className="mt-1 text-sm text-neutral-600">
                  {runScopeDescription(run.scope)}
                </p>
              )}
              {run.trigger && (
                <p className="mt-1 text-sm text-neutral-600">
                  {runTriggerDescription(run.trigger)}
                </p>
              )}
              {run.state_op && (
                <p className="mt-1 text-sm text-neutral-600">
                  <span className="text-neutral-500">
                    {stateOpDescription(run.state_op)}
                  </span>{" "}
                  <code className="bg-neutral-100 px-1.5 py-0.5 font-mono text-xs text-neutral-900">
                    {run.state_op.command}
                  </code>
                </p>
              )}
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
                started {new Date(run.created_at).toLocaleString()}
                {run.started_by && <> by {run.started_by}</>} ·{" "}
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
          {canApprove &&
            run.action === "uninstall" &&
            run.status === "succeeded" &&
            !run.scope &&
            appName && (
              <DeleteAfterUninstall
                appId={appId}
                appName={appName}
                runId={runId}
              />
            )}

          {/* The rail of stages → components → steps on the left (above, on a
              narrow screen) and the selected step's pane filling the rest.
              Expanding the pane hides the rail. */}
          <div className="flex min-h-0 flex-1 flex-col border border-neutral-200 md:flex-row">
            {!expanded && (
              <RunRail
                stages={stages}
                stepById={stepById}
                selectedId={selectedRunId}
                onSelect={selectStep}
              />
            )}
            {selectedRunId ? (
              <ComponentRunPanel
                key={selectedRunId}
                appId={appId}
                runId={runId}
                componentRunId={selectedRunId}
                // The live status of the selected component run, folded from the
                // run stream. Passing it as a key into the panel's fetch effect
                // forces a re-fetch when the step transitions (e.g. to a terminal
                // state), so logs/diff populate without a manual reselect.
                liveStatus={stepById.get(selectedRunId)?.status}
                isPreview={isReadOnlyAction(run.action)}
                isDrift={run.action === "drift"}
                stateOp={run.state_op ?? null}
                policy={selectedPolicy}
                startedBy={run.started_by ?? ""}
                planRun={planSource}
                canApprove={canApprove}
                onDecided={load}
                expanded={expanded}
                onToggleExpanded={() => setExpanded((e) => !e)}
              />
            ) : (
              <p className="p-4 text-sm text-neutral-500">
                This run has no steps.
              </p>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// runStagesOf returns the run's stage summary. A payload without one (it is
// always present from this server; this only guards a partial response) falls
// back to a single stage listing each step as its own component.
function runStagesOf(run: WorkflowRunDetail): RunStage[] {
  if (run.stages) return run.stages;
  const steps = run.component_runs ?? [];
  if (steps.length === 0) return [];
  return [
    {
      name: "Stage 1",
      status: combineStatuses(steps.map((s) => s.status)),
      components: steps.map((s) => ({
        component_id: s.component_id ?? s.id,
        name: s.name ?? "(unnamed)",
        type: s.type ?? "helm",
        status: s.status,
        component_run_ids: [s.id],
      })),
    },
  ];
}

// combineStatuses folds step statuses the way the server's stage summary does:
// failed, then awaiting approval, then running (or a mix of settled and
// pending, which is mid-flight), then all pending, then skipped, else
// succeeded.
function combineStatuses(statuses: ComponentRunStatus[]): ComponentRunStatus {
  const has = new Set(statuses);
  if (has.has("failed")) return "failed";
  if (has.has("awaiting_approval")) return "awaiting_approval";
  if (has.has("running")) return "running";
  if (has.has("pending")) return has.size > 1 ? "running" : "pending";
  if (has.has("skipped")) return "skipped";
  return "succeeded";
}

// mostRelevantStep picks the step the view follows when none is chosen: one
// waiting on a person, else one that failed, else one running, else the last
// one that ran, else the first.
function mostRelevantStep(steps: ComponentRun[]): ComponentRun | undefined {
  for (const want of ["awaiting_approval", "failed", "running"] as const) {
    const s = steps.find((x) => x.status === want);
    if (s) return s;
  }
  const ran = steps.filter(
    (s) => s.status !== "pending" && s.status !== "skipped",
  );
  return ran[ran.length - 1] ?? steps[0];
}

// primaryStep picks the step a component's row opens: the one that failed or
// is waiting/running, else the later step once it has run (an OpenTofu
// component's apply), else the first (its plan).
function primaryStep(steps: ComponentRun[]): ComponentRun | undefined {
  for (const want of ["failed", "awaiting_approval", "running"] as const) {
    const s = steps.find((x) => x.status === want);
    if (s) return s;
  }
  const last = steps[steps.length - 1];
  if (last && last.status !== "pending" && last.status !== "skipped")
    return last;
  return steps[0];
}

// stepLabel is a step's own label under its component: an OpenTofu step's
// name carries the component name plus " · plan" / " · apply" (or a state
// operation), so the part after the last separator is the step.
function stepLabel(name: string | undefined): string {
  if (!name) return "step";
  const i = name.lastIndexOf(" · ");
  return i >= 0 ? name.slice(i + 3) : name;
}

// elapsed is a step's run time so far (or in total once it finished), or ""
// before it starts.
function elapsed(start?: string | null, end?: string | null): string {
  if (!start) return "";
  return formatDuration(start, end ?? undefined).replace(" (running)", "");
}

// componentElapsed spans a component's steps: from the first start to the
// last finish (or now, while one is still going).
function componentElapsed(steps: ComponentRun[]): string {
  const starts = steps.map((s) => s.started_at).filter((x): x is string => !!x);
  if (starts.length === 0) return "";
  const start = starts.sort()[0];
  const done = steps.every(
    (s) => s.finished_at || s.status === "skipped" || s.status === "pending",
  );
  const ends = steps.map((s) => s.finished_at).filter((x): x is string => !!x);
  return elapsed(start, done && ends.length > 0 ? ends.sort()[ends.length - 1] : null);
}

// RunRail lists the run's stages, each with its components; a component with
// more than one step (an OpenTofu plan + apply) lists the steps under it.
// Every row selects a step.
function RunRail({
  stages,
  stepById,
  selectedId,
  onSelect,
}: {
  stages: RunStage[];
  stepById: Map<string, ComponentRun>;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <nav
      aria-label="Run steps"
      className="max-h-56 shrink-0 overflow-y-auto border-b border-neutral-200 bg-neutral-50 pb-2 md:max-h-none md:w-72 md:border-b-0 md:border-r"
    >
      {stages.map((st, i) => (
        <section key={`${i}-${st.name}`} aria-label={`Stage ${st.name}`}>
          <h2 className="flex items-center gap-2 px-3 pb-1 pt-3 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
            <ComponentStatusIcon status={st.status} />
            <span className="truncate">{st.name}</span>
          </h2>
          <ul>
            {st.components.map((c) => (
              <RailComponent
                key={c.component_id}
                component={c}
                stepById={stepById}
                selectedId={selectedId}
                onSelect={onSelect}
              />
            ))}
          </ul>
        </section>
      ))}
    </nav>
  );
}

function RailComponent({
  component,
  stepById,
  selectedId,
  onSelect,
}: {
  component: RunStageComponent;
  stepById: Map<string, ComponentRun>;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  const steps = component.component_run_ids
    .map((id) => stepById.get(id))
    .filter((s): s is ComponentRun => s != null);
  const multi = steps.length > 1;
  const selected = !multi && steps.some((s) => s.id === selectedId);
  const primary = primaryStep(steps);
  const plan = !multi ? steps[0]?.plan : undefined;
  return (
    <li>
      <button
        type="button"
        onClick={() => primary && onSelect(primary.id)}
        disabled={!primary}
        aria-current={selected ? "true" : undefined}
        className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm ${
          selected ? "bg-white font-medium ring-1 ring-inset ring-neutral-300" : "hover:bg-white"
        }`}
      >
        <ComponentStatusIcon status={component.status} />
        <span
          className={`min-w-0 flex-1 truncate ${
            component.status === "skipped"
              ? "text-neutral-500 line-through"
              : "text-neutral-900"
          }`}
        >
          {component.name}
        </span>
        {plan && <PlanCounts plan={plan} />}
        <TypeBadge type={component.type as ComponentType} />
        <span className="w-12 shrink-0 text-right text-xs text-neutral-400">
          {componentElapsed(steps)}
        </span>
      </button>
      {multi && (
        <ul>
          {steps.map((s) => {
            const on = s.id === selectedId;
            return (
              <li key={s.id}>
                <button
                  type="button"
                  onClick={() => onSelect(s.id)}
                  aria-current={on ? "true" : undefined}
                  className={`flex w-full items-center gap-2 py-1 pl-9 pr-3 text-left text-xs ${
                    on
                      ? "bg-white font-medium ring-1 ring-inset ring-neutral-300"
                      : "text-neutral-600 hover:bg-white"
                  }`}
                >
                  <ComponentStatusIcon status={s.status} />
                  <span className="min-w-0 flex-1 truncate">
                    {stepLabel(s.name)}
                  </span>
                  {s.plan && <PlanCounts plan={s.plan} />}
                  <span className="w-12 shrink-0 text-right text-neutral-400">
                    {elapsed(s.started_at, s.finished_at)}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

// ComponentRunPanel is the main pane for one component run (step): its logs
// (and for preview runs, its diff; for a tofu apply step, its upstream plan
// output) under a compact header, with the content area filling whatever room
// the pane has — beside the rail by default, or the whole view when expanded.
function ComponentRunPanel({
  appId,
  runId,
  componentRunId,
  liveStatus,
  isPreview,
  isDrift,
  stateOp,
  policy,
  startedBy,
  planRun,
  canApprove,
  onDecided,
  expanded,
  onToggleExpanded,
}: {
  appId: string;
  runId: string;
  componentRunId: string;
  // The live status from the run stream; when it changes (notably as the step
  // settles) the fetch effect re-runs so the detail (logs/diff) stays current.
  liveStatus?: ComponentRunStatus;
  // A read-only run (preview or drift check): the step's finding leads.
  isPreview: boolean;
  // A drift check specifically: the finding is drift, not a deploy diff.
  isDrift: boolean;
  // The state operation of a state_op run: its gate shows the exact command
  // the step will run, since there is no plan to review.
  stateOp: StateOperation | null;
  // The step's approval policy (from the run snapshot) and who started the
  // run, so the gate can explain who may approve and show the N-of-M tally.
  policy: ApprovalPolicy | null;
  startedBy: string;
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
}) {
  const [detail, setDetail] = useState<ComponentRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // The upstream plan step's detail (its parsed plan + body), for an apply step.
  const [planDetail, setPlanDetail] = useState<ComponentRunDetail | null>(null);
  const [deciding, setDeciding] = useState(false);
  const [decideError, setDecideError] = useState<string | null>(null);
  // For preview runs the diff is what the user came to inspect, so it leads; a
  // parked apply step opens on its plan output (the thing being approved);
  // everything else opens on logs. (The panel remounts per selection via key.)
  const [tab, setTab] = useState<
    "logs" | "diff" | "plan" | "outputs" | "resources"
  >(
    isPreview
      ? "diff"
      : planRun && liveStatus === "awaiting_approval"
        ? "plan"
        : "logs",
  );

  // The gate is live (open) only while the step is parked. Once the stream folds
  // a resumed status in, liveStatus changes and the buttons drop away.
  const awaitingApproval = liveStatus === "awaiting_approval";

  // Captured OpenTofu outputs (a terraform apply step settled on a deploy run),
  // sorted by name for a stable display. Empty for every other step.
  const outputEntries = Object.entries(detail?.outputs ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  // The managed-resource inventory a settled apply step recorded alongside
  // its outputs.
  const hasResources = (detail?.resources?.length ?? 0) > 0;

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
      if (!cancelled && data) setPlanDetail(data);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, runId, planRunId]);

  // While the step is running, follow its pod output live (editor+ only — the
  // stream is gated like the captured logs). Once it settles, the detail
  // refetch brings the durable captured logs, which take over below.
  const live = liveStatus === "running" && !!detail?.run_name && canApprove;
  const liveLogs = usePodLogs(
    `/api/applications/${appId}/runs/${runId}/components/${componentRunId}/logs/stream`,
    live,
  );

  // A settled OpenTofu plan step carries its own parsed plan (on any action):
  // that is the step's content, so it gets a Plan tab that leads once loaded.
  const ownPlan = !isPreview && !planRun ? (detail?.plan ?? null) : null;
  useEffect(() => {
    if (ownPlan) setTab((t) => (t === "logs" ? "plan" : t));
  }, [ownPlan]);

  return (
    <section
      aria-label="Selected step"
      className="flex min-h-0 min-w-0 flex-1 flex-col bg-white"
    >
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
                ? "Shrink the panel and show the run's steps again"
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
                      : stateOp
                        ? "Approving runs this command against the component's state (nothing is planned first):"
                        : "Approve to run this step, or reject to fail the run."}
                  </p>
                  {stateOp && (
                    <code
                      data-testid="state-op-command"
                      className="mt-1.5 block w-fit bg-white px-2 py-1 font-mono text-xs text-neutral-900 ring-1 ring-violet-200"
                    >
                      {stateOp.command}
                    </code>
                  )}
                  <ApprovalPolicyLine
                    policy={policy}
                    approvals={detail.approvals ?? []}
                    startedBy={startedBy}
                  />
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

          {/* The policy verdict recorded on an OpenTofu plan step (shown on the
              step itself and at its apply's approval gate). */}
          {(detail?.policy ?? planDetail?.policy) && (
            <PolicyVerdictBox verdict={(detail?.policy ?? planDetail?.policy)!} />
          )}

          {/* Preview runs get a Diff/Logs tab pair, a tofu apply step a
              Plan output(/Outputs)/Logs set, so each view spans the whole
              panel; other runs are logs-only with no tab chrome. */}
          {isPreview && (
            <div className="flex items-center gap-4 border-b border-neutral-200 px-4">
              <TabButton active={tab === "diff"} onClick={() => setTab("diff")}>
                {isDrift ? "Drift" : "Preview diff"}
                <ChangesBadge
                  hasChanges={detail.has_changes}
                  words={isDrift ? ["drift", "no drift"] : undefined}
                />
              </TabButton>
              <TabButton active={tab === "logs"} onClick={() => setTab("logs")}>
                Logs
              </TabButton>
            </div>
          )}
          {!isPreview &&
            (planRun ||
              ownPlan ||
              outputEntries.length > 0 ||
              hasResources) && (
              <div className="flex items-center gap-4 border-b border-neutral-200 px-4">
                {planRun && (
                  <TabButton
                    active={tab === "plan"}
                    onClick={() => setTab("plan")}
                  >
                    Plan output
                    {planDetail?.plan && <PlanCounts plan={planDetail.plan} />}
                  </TabButton>
                )}
                {ownPlan && (
                  <TabButton
                    active={tab === "plan"}
                    onClick={() => setTab("plan")}
                  >
                    Plan
                    <PlanCounts plan={ownPlan} />
                  </TabButton>
                )}
                {outputEntries.length > 0 && (
                  <TabButton
                    active={tab === "outputs"}
                    onClick={() => setTab("outputs")}
                  >
                    Outputs
                  </TabButton>
                )}
                {hasResources && (
                  <TabButton
                    active={tab === "resources"}
                    onClick={() => setTab("resources")}
                  >
                    Resources
                    <span className="text-xs text-neutral-400">
                      {detail.resources?.length}
                    </span>
                  </TabButton>
                )}
                <TabButton
                  active={tab === "logs"}
                  onClick={() => setTab("logs")}
                >
                  Logs
                </TabButton>
              </div>
            )}

          <div className="min-h-0 flex-1 p-3">
            {isPreview && tab === "diff" ? (
              detail.plan ? (
                <PlanBody plan={detail.plan} body={detail.diff} />
              ) : detail.diff ? (
                <DiffView diff={detail.diff} className="h-full" />
              ) : (
                <p className="text-sm text-neutral-500">
                  {detail.has_changes === false
                    ? isDrift
                      ? "No drift detected."
                      : "No changes."
                    : isDrift
                      ? "No drift report captured."
                      : "No diff captured."}
                </p>
              )
            ) : planRun && tab === "plan" ? (
              planDetail === null ? (
                <p className="text-sm text-neutral-500">
                  Loading plan output from {planRun.name}…
                </p>
              ) : planDetail.plan ? (
                <PlanBody plan={planDetail.plan} body={planDetail.diff} />
              ) : (
                <pre className="h-full w-full overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                  {planDetail.logs || "No plan output was captured."}
                </pre>
              )
            ) : ownPlan && tab === "plan" ? (
              <PlanBody plan={ownPlan} body={detail.diff} />
            ) : outputEntries.length > 0 && tab === "outputs" ? (
              <OutputsTable entries={outputEntries} />
            ) : hasResources && tab === "resources" ? (
              <ResourcesTable resources={detail.resources ?? []} />
            ) : live ? (
              <pre
                data-testid="live-logs"
                className="h-full w-full overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100"
              >
                {liveLogs.lines.length > 0
                  ? liveLogs.lines.join("\n")
                  : liveLogs.error
                    ? `Waiting for output… (${liveLogs.error})`
                    : "Waiting for output…"}
              </pre>
            ) : (
              <pre className="h-full w-full overflow-auto bg-neutral-950 p-3 font-mono text-xs leading-relaxed text-neutral-100">
                {detail.logs ||
                  (liveLogs.lines.length > 0
                    ? liveLogs.lines.join("\n")
                    : "No logs were captured for this step.")}
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

// PlanBody is the structured reading of an OpenTofu plan: the headline totals,
// the per-resource action list (each expandable to its own diff block), and
// the full plan text underneath for anyone who wants tofu's own words. body is
// the plan text the API supplies to editors; below editor it is absent and the
// resource rows are not expandable.
function PlanBody({ plan, body }: { plan: PlanSummary; body?: string }) {
  const [showText, setShowText] = useState(false);
  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-auto">
      <PlanSummaryBar plan={plan} />
      <PlanResourceList plan={plan} />
      {body && (
        <div>
          <button
            type="button"
            onClick={() => setShowText((s) => !s)}
            className="text-xs text-neutral-500 underline-offset-2 hover:text-neutral-900 hover:underline"
          >
            {showText ? "Hide full plan text" : "Show full plan text"}
          </button>
          {showText && <DiffView diff={body} className="mt-2 max-h-[40rem]" />}
        </div>
      )}
    </div>
  );
}

function ChangesBadge({
  hasChanges,
  words = ["changes", "no changes"],
}: {
  hasChanges?: boolean;
  // The [positive, negative] wording — a drift check says drift / no drift.
  words?: [string, string];
}) {
  if (hasChanges === undefined) return null;
  return hasChanges ? (
    <span className="inline-flex items-center px-2 py-0.5 text-xs font-medium bg-amber-100 text-amber-800">
      {words[0]}
    </span>
  ) : (
    <span className="inline-flex items-center px-2 py-0.5 text-xs font-medium bg-neutral-100 text-neutral-600">
      {words[1]}
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

// ApprovalPolicyLine explains a parked gate's policy: who may approve, the
// N-of-M tally so far, whether the starter is excluded, and the timeout.
// Renders nothing for the default policy with no approvals yet.
function ApprovalPolicyLine({
  policy,
  approvals,
  startedBy,
}: {
  policy: ApprovalPolicy | null;
  approvals: { by: string; at: string }[];
  startedBy: string;
}) {
  const required = Math.max(1, policy?.required ?? 1);
  const parts: string[] = [];
  if (policy?.approvers?.length) {
    parts.push(`Approvers: ${policy.approvers.join(", ")}`);
  }
  if (required > 1 || approvals.length > 0) {
    parts.push(
      `${approvals.length} of ${required} approval${required === 1 ? "" : "s"}` +
        (approvals.length > 0
          ? ` (${approvals.map((a) => a.by).join(", ")})`
          : ""),
    );
  }
  if (policy?.require_different_approver && startedBy) {
    parts.push(`${startedBy} started this run and cannot approve it`);
  }
  if (policy?.timeout_minutes) {
    parts.push(`times out after ${policy.timeout_minutes} min`);
  }
  if (parts.length === 0) return null;
  return (
    <p data-testid="approval-policy" className="mt-1.5 text-xs text-violet-800">
      {parts.join(" · ")}
    </p>
  );
}

// PolicyVerdictBox lists every policy evaluated against a plan: its
// enforcement, and its violations or evaluation error. Blocked reads red,
// warned amber, clean green.
function PolicyVerdictBox({ verdict }: { verdict: PolicyVerdict }) {
  const tone = verdict.blocked
    ? "border-red-200 bg-red-50 text-red-900"
    : verdict.warned
      ? "border-amber-200 bg-amber-50 text-amber-900"
      : "border-emerald-200 bg-emerald-50 text-emerald-900";
  const title = verdict.blocked
    ? "Blocked by policy"
    : verdict.warned
      ? "Policy warnings"
      : "Policies passed";
  return (
    <div data-testid="policy-verdict" className={`border-b px-4 py-3 text-sm ${tone}`}>
      <p className="font-medium">{title}</p>
      <ul className="mt-1 flex flex-col gap-0.5 text-xs">
        {verdict.results.map((r) => (
          <li key={r.policy_id}>
            <span className="font-medium">{r.policy_name}</span>{" "}
            <span className="opacity-70">({r.enforcement})</span>
            {r.error
              ? ` — could not be evaluated: ${r.error}`
              : r.violations.length === 0
                ? " — passed"
                : ` — ${r.violations.join("; ")}`}
          </li>
        ))}
      </ul>
    </div>
  );
}

// DeleteAfterUninstall offers the application's delete once its uninstall has
// succeeded — the second half of the Delete dialog's "Uninstall it first"
// (which only starts the uninstall: it is an ordinary run and may wait on
// approvals). Shown only while this uninstall is still the application's
// latest run; after a later deploy the offer would be wrong.
function DeleteAfterUninstall({
  appId,
  appName,
  runId,
}: {
  appId: string;
  appName: string;
  runId: string;
}) {
  const navigate = useNavigate();
  const [isLatest, setIsLatest] = useState(false);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}/runs", {
        params: { path: { id: appId } },
      });
      if (!cancelled) setIsLatest(data?.runs?.[0]?.id === runId);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, runId]);

  if (!isLatest) return null;
  return (
    <div className="mb-3 flex flex-wrap items-center justify-between gap-3 border border-neutral-200 bg-white px-4 py-3">
      <p className="text-sm text-neutral-700">
        Everything <span className="font-medium">{appName}</span> deployed has
        been uninstalled. You can delete the application now.
      </p>
      <button
        type="button"
        onClick={() => setDeleting(true)}
        className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50"
      >
        <Trash2 className="h-3.5 w-3.5" />
        Delete application
      </button>
      {deleting && (
        <DeleteApplicationDialog
          app={{ id: appId, name: appName }}
          afterUninstall
          onClose={() => setDeleting(false)}
          onDeleted={() => navigate("/applications")}
        />
      )}
    </div>
  );
}
