import { useCallback, useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { formatDuration } from "../lib/duration";
import { RunStatusBadge } from "../components/workflow/status";
import type { StreamStatus } from "../lib/resourceStream";
import { useObjectStream } from "../lib/useObjectStream";

type WorkflowRun = components["schemas"]["WorkflowRun"];
type RunList = components["schemas"]["RunList"];
type Application = components["schemas"]["Application"];

// Sentinel for an unselected filter (shown as "All …").
const ALL = "__all__";

// RunsIndex is the Applications › Workflow Runs page (route /runs): the global
// deploy history across every application in the organization, newest-first and
// updated live. Two filters narrow the table — application and runner cluster —
// and each filter is a URL query param so an application can deep-link here
// pre-filtered to itself (?application=<id>). Every run carries its
// application_id; the application's name and its runner cluster are resolved
// client-side from the applications and clusters lists (the same join the
// application detail page does), so no enriched run payload is needed. The deploy
// target lives on the individual components, not the application, so the runs
// table no longer shows a single target cluster.
export function RunsIndex() {
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();

  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [appsById, setAppsById] = useState<Record<string, Application>>({});
  const [clusterNameById, setClusterNameById] = useState<Record<string, string>>(
    {},
  );
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Selected filters live in the URL so the view is shareable and an app page can
  // link here pre-filtered. Absent params mean "all".
  const appFilter = searchParams.get("application") ?? ALL;
  const runnerFilter = searchParams.get("runner_cluster") ?? ALL;

  const setFilter = useCallback(
    (key: string, value: string) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (value === ALL) next.delete(key);
          else next.set(key, value);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  // Initial load: applications and clusters (for names + filter options) plus a
  // first page of runs for instant paint. The live stream below then takes over.
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [appsRes, clustersRes, runsRes] = await Promise.all([
      api.GET("/api/applications"),
      api.GET("/api/clusters"),
      api.GET("/api/runs"),
    ]);
    if (runsRes.error || !runsRes.data) {
      setError(runsRes.error?.message ?? "Could not load runs");
      setLoading(false);
      return;
    }
    const apps: Record<string, Application> = {};
    for (const a of appsRes.data ?? []) apps[a.id] = a;
    const clusters: Record<string, string> = {};
    for (const c of clustersRes.data ?? []) clusters[c.id] = c.name;
    setAppsById(apps);
    setClusterNameById(clusters);
    setRuns(runsRes.data.runs);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Live updates: the org-wide stream pushes the full (capped) list on any run's
  // progress. Treat each snapshot as the latest value, replacing the table.
  const { value: streamed, status } = useObjectStream<RunList>(
    "/api/runs/stream",
    !loading,
  );
  useEffect(() => {
    if (streamed) setRuns(streamed.runs);
  }, [streamed]);

  // The runner cluster of a run is its application's, resolved client-side.
  const runnerClusterId = useCallback(
    (r: WorkflowRun) => appsById[r.application_id]?.runner_cluster_id,
    [appsById],
  );

  // Filter options are derived from the runs actually present, so each dropdown
  // only offers values that can match something.
  const appOptions = useMemo(() => {
    const ids = new Set(runs.map((r) => r.application_id));
    return [...ids]
      .map((id) => ({ id, name: appsById[id]?.name ?? id }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [runs, appsById]);

  const runnerClusterOptions = useMemo(
    () => clusterOptions(runs.map(runnerClusterId), clusterNameById),
    [runs, runnerClusterId, clusterNameById],
  );

  const visibleRuns = useMemo(
    () =>
      runs.filter(
        (r) =>
          (appFilter === ALL || r.application_id === appFilter) &&
          (runnerFilter === ALL || runnerClusterId(r) === runnerFilter),
      ),
    [runs, appFilter, runnerFilter, runnerClusterId],
  );

  const filtered = appFilter !== ALL || runnerFilter !== ALL;

  return (
    <div>
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Applications
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Workflow Runs</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Deploy history across every application, updated live.
          </p>
        </div>
        {!loading && runs.length > 0 && (
          <div className="flex flex-wrap items-center justify-end gap-3">
            <LiveIndicator status={status} />
            <FilterSelect
              label="Application"
              value={appFilter}
              allLabel="All applications"
              options={appOptions}
              onChange={(v) => setFilter("application", v)}
            />
            <FilterSelect
              label="Runner cluster"
              value={runnerFilter}
              allLabel="All runner clusters"
              options={runnerClusterOptions}
              onChange={(v) => setFilter("runner_cluster", v)}
            />
          </div>
        )}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : runs.length === 0 ? (
        <p className="mt-6 text-sm text-neutral-500">
          No runs yet. Start one from an application's workflow page.
        </p>
      ) : visibleRuns.length === 0 ? (
        <p className="mt-6 text-sm text-neutral-500">
          No runs match the selected filters.
        </p>
      ) : (
        <div className="mt-6 border border-neutral-200 bg-white">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Application</th>
                <th className="px-4 py-2 font-medium">Runner cluster</th>
                <th className="px-4 py-2 font-medium">Action</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Started</th>
                <th className="px-4 py-2 font-medium">Duration</th>
              </tr>
            </thead>
            <tbody>
              {visibleRuns.map((r) => (
                <tr
                  key={r.id}
                  // Carry this page (with its filters) as the run view's Back
                  // target, so Back returns here rather than to the application.
                  onClick={() =>
                    navigate(`/applications/${r.application_id}/runs/${r.id}`, {
                      state: {
                        from: `${location.pathname}${location.search}`,
                      },
                    })
                  }
                  className="cursor-pointer border-b border-neutral-100 last:border-0 hover:bg-neutral-50"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    {appsById[r.application_id]?.name ?? "—"}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {clusterLabel(runnerClusterId(r), clusterNameById)}
                  </td>
                  <td className="px-4 py-3 capitalize text-neutral-600">
                    {r.action}
                  </td>
                  <td className="px-4 py-3">
                    <RunStatusBadge status={r.status} />
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {new Date(r.created_at).toLocaleString()}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {formatDuration(r.created_at, r.finished_at ?? undefined)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {runs.length >= 200 && (
            <p className="border-t border-neutral-100 px-4 py-2 text-xs text-neutral-400">
              Showing the {runs.length} most recent runs
              {filtered ? " (filters applied to this set)" : ""}.
            </p>
          )}
        </div>
      )}
    </div>
  );
}

// clusterOptions builds a sorted, de-duplicated {id, name} list from a set of
// cluster ids (ignoring the undefined ones), resolving names where known.
function clusterOptions(
  ids: (string | undefined)[],
  names: Record<string, string>,
): { id: string; name: string }[] {
  const set = new Set<string>();
  for (const id of ids) if (id) set.add(id);
  return [...set]
    .map((id) => ({ id, name: names[id] ?? id }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

// clusterLabel resolves a cluster id to its name, falling back to "—" when the
// run's application or its cluster can't be resolved.
function clusterLabel(
  id: string | undefined,
  names: Record<string, string>,
): string {
  if (!id) return "—";
  return names[id] ?? id;
}

function FilterSelect({
  label,
  value,
  allLabel,
  options,
  onChange,
}: {
  label: string;
  value: string;
  allLabel: string;
  options: { id: string; name: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-neutral-600">
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="border border-neutral-300 bg-white px-2 py-1.5 text-sm text-neutral-900 focus:border-black focus:outline-none"
      >
        <option value={ALL}>{allLabel}</option>
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
    </label>
  );
}

function LiveIndicator({ status }: { status: StreamStatus }) {
  const config: Record<
    StreamStatus,
    { label: string; dot: string; text: string; pulse: boolean }
  > = {
    live: { label: "Live", dot: "bg-green-500", text: "text-neutral-600", pulse: false },
    connecting: {
      label: "Connecting…",
      dot: "bg-neutral-400",
      text: "text-neutral-500",
      pulse: true,
    },
    reconnecting: {
      label: "Reconnecting…",
      dot: "bg-amber-500",
      text: "text-amber-700",
      pulse: true,
    },
    error: {
      label: "Disconnected",
      dot: "bg-red-500",
      text: "text-red-700",
      pulse: false,
    },
  };
  const c = config[status];
  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs font-medium ${c.text}`}
    >
      <span
        className={`h-2 w-2 rounded-full ${c.dot} ${c.pulse ? "animate-pulse" : ""}`}
      />
      {c.label}
    </span>
  );
}
