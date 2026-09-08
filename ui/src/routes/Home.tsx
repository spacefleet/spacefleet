import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AlertTriangle, Boxes, History, Server } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { RunStatusBadge } from "../components/workflow/status";
import { runActionLabel } from "../components/workflow/runAction";

type Application = components["schemas"]["Application"];
type Cluster = components["schemas"]["Cluster"];
type WorkflowRun = components["schemas"]["WorkflowRun"];

const IN_FLIGHT = new Set(["pending", "running", "awaiting_approval"]);
const RECENT = 8;

// Home is the organization dashboard: the counts that matter (applications,
// clusters, runs in flight), the runs that need a person — parked at an
// approval gate or recently failed — and the latest runs across every
// application. Everything links into the pages that act on it.
export function Home() {
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const [apps, setApps] = useState<Application[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [appsRes, clustersRes, runsRes] = await Promise.all([
      api.GET("/api/applications"),
      api.GET("/api/clusters"),
      api.GET("/api/runs"),
    ]);
    const failed = appsRes.error ?? clustersRes.error ?? runsRes.error;
    if (failed) setError(failed.message ?? "Could not load the dashboard");
    setApps(appsRes.data ?? []);
    setClusters(clustersRes.data ?? []);
    setRuns(runsRes.data?.runs ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  const appName = useMemo(() => {
    const byId: Record<string, string> = {};
    for (const a of apps) byId[a.id] = a.name;
    return (id: string) => byId[id] ?? "—";
  }, [apps]);

  const inFlight = runs.filter((r) => IN_FLIGHT.has(r.status));
  const awaiting = runs.filter((r) => r.status === "awaiting_approval");
  const failed = runs
    .filter((r) => r.status === "failed" || r.status === "partial")
    .slice(0, 5);
  const recent = runs.slice(0, RECENT);

  return (
    <div>
      <h1 className="text-2xl font-bold tracking-tight">
        {currentOrg?.name ?? "No organization"}
      </h1>
      <p className="mt-1 text-sm text-neutral-600">
        {currentRole ? `You are a${currentRole === "admin" || currentRole === "editor" ? "n" : ""} ${currentRole} here.` : ""}{" "}
        Switch organizations from the menu in the top bar.
      </p>

      {error && <p className="mt-4 text-sm text-red-600">{error}</p>}

      <div className="mt-6 grid gap-4 sm:grid-cols-3">
        <StatTile
          icon={<Boxes className="h-4 w-4" />}
          label="Applications"
          value={loading ? "…" : String(apps.length)}
          to="/applications"
        />
        <StatTile
          icon={<Server className="h-4 w-4" />}
          label="Clusters"
          value={loading ? "…" : String(clusters.length)}
          to="/admin/clusters"
        />
        <StatTile
          icon={<History className="h-4 w-4" />}
          label="Runs in flight"
          value={loading ? "…" : String(inFlight.length)}
          to="/runs"
        />
      </div>

      {(awaiting.length > 0 || failed.length > 0) && (
        <div className="mt-6 border border-amber-200 bg-amber-50">
          <div className="flex items-center gap-2 border-b border-amber-200 px-4 py-2">
            <AlertTriangle className="h-3.5 w-3.5 text-amber-700" />
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-amber-800">
              Needs attention
            </h2>
          </div>
          <ul className="divide-y divide-amber-200/60">
            {awaiting.map((r) => (
              <RunRow key={r.id} run={r} appName={appName(r.application_id)} note="waiting for approval" onOpen={navigate} />
            ))}
            {failed.map((r) => (
              <RunRow key={r.id} run={r} appName={appName(r.application_id)} note={r.message ?? ""} onOpen={navigate} />
            ))}
          </ul>
        </div>
      )}

      <div className="mt-6 border border-neutral-200 bg-white">
        <div className="flex items-center justify-between border-b border-neutral-200 px-4 py-2">
          <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
            Recent runs
          </h2>
          <Link to="/runs" className="text-xs text-neutral-500 underline-offset-2 hover:underline">
            All runs
          </Link>
        </div>
        {loading ? (
          <p className="px-4 py-6 text-sm text-neutral-500">Loading…</p>
        ) : recent.length === 0 ? (
          <p className="px-4 py-6 text-sm text-neutral-500">
            No runs yet.{" "}
            {apps.length === 0 ? (
              <>
                <Link to="/applications" className="underline-offset-2 hover:underline">
                  Create an application
                </Link>{" "}
                to get started.
              </>
            ) : (
              "Start one from an application's page."
            )}
          </p>
        ) : (
          <ul className="divide-y divide-neutral-100">
            {recent.map((r) => (
              <RunRow key={r.id} run={r} appName={appName(r.application_id)} note="" onOpen={navigate} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function StatTile({
  icon,
  label,
  value,
  to,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  to: string;
}) {
  return (
    <Link to={to} className="border border-neutral-200 bg-white px-4 py-3 hover:bg-neutral-50">
      <p className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-wide text-neutral-400">
        {icon}
        {label}
      </p>
      <p className="mt-1 text-2xl font-bold tracking-tight text-neutral-900">{value}</p>
    </Link>
  );
}

function RunRow({
  run,
  appName,
  note,
  onOpen,
}: {
  run: WorkflowRun;
  appName: string;
  note: string;
  onOpen: (path: string) => void;
}) {
  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(`/applications/${run.application_id}/runs/${run.id}`)}
        className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left text-sm hover:bg-neutral-50"
      >
        <span className="flex min-w-0 items-center gap-3">
          <span className="font-medium text-neutral-900">{appName}</span>
          <span className="capitalize text-neutral-600">{runActionLabel(run.action, run.scope)}</span>
          <RunStatusBadge status={run.status} />
          {note && <span className="truncate text-xs text-neutral-500">{note}</span>}
        </span>
        <span className="shrink-0 text-xs text-neutral-500">
          {new Date(run.created_at).toLocaleString()}
        </span>
      </button>
    </li>
  );
}
