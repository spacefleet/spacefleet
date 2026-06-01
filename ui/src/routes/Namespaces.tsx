import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AlertTriangle, Boxes, CircleSlash, Server } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { namespacePhase } from "../lib/namespaces";
import { nodeAge, type Cluster } from "../lib/nodes";
import { useClusterNamespaceStreams } from "../lib/useClusterNamespaceStreams";
import type { StreamStatus } from "../lib/resourceStream";

const ALL = "all";

// Namespaces is the Infrastructure › Namespaces page. Like Nodes, it lists a
// cluster-level resource of the organization's registered clusters — by default
// every namespace across every connected cluster, filterable to a single
// cluster — and updates live over SSE (see lib/useClusterNamespaceStreams), so
// the table reflects changes without a manual refresh. Each row links to a
// detailed view.
export function Namespaces() {
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [clustersLoading, setClustersLoading] = useState(true);
  const [clustersError, setClustersError] = useState<string | null>(null);
  const [filter, setFilter] = useState<string>(ALL);

  // The cluster list itself is fetched once (it changes rarely, via the
  // Clusters page); only the namespaces within them stream live.
  const loadClusters = useCallback(async () => {
    setClustersLoading(true);
    setClustersError(null);
    const { data, error } = await api.GET("/api/clusters");
    if (error) setClustersError(error.message ?? "Could not load clusters");
    setClusters(data ?? []);
    setFilter(ALL);
    setClustersLoading(false);
  }, []);

  useEffect(() => {
    void loadClusters();
  }, [loadClusters, currentOrg?.id]);

  // The clusters we watch: the chosen one when filtered, otherwise every
  // connected cluster (an unconnected cluster can't serve namespaces).
  const targets = useMemo(() => {
    if (filter !== ALL) return clusters.filter((c) => c.id === filter);
    return clusters.filter((c) => c.status === "connected");
  }, [clusters, filter]);

  const { rows, status, errors } = useClusterNamespaceStreams(targets);

  const sortedRows = useMemo(
    () =>
      [...rows].sort(
        (a, b) =>
          a.clusterName.localeCompare(b.clusterName) ||
          a.name.localeCompare(b.name),
      ),
    [rows],
  );

  // Clusters that exist but were skipped in the "all" view because they aren't
  // connected — surfaced so an empty table isn't mistaken for "no namespaces".
  const skipped =
    filter === ALL ? clusters.filter((c) => c.status !== "connected").length : 0;

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Infrastructure
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Namespaces</h1>
          <p className="mt-1 text-sm text-gray-600">
            The Kubernetes namespaces across your registered clusters, updated
            live.
          </p>
        </div>
        {!clustersLoading && clusters.length > 0 && (
          <div className="flex items-center gap-3">
            {targets.length > 0 && <LiveIndicator status={status} />}
            <label className="flex items-center gap-2 text-sm text-gray-600">
              Cluster
              <select
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className="border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-900 focus:border-black focus:outline-none"
              >
                <option value={ALL}>All clusters</option>
                {clusters.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
          </div>
        )}
      </div>

      {/* Per-cluster stream failures (e.g. an unreachable cluster). */}
      {errors.length > 0 && (
        <div className="mt-4 border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
          {errors.map((e) => (
            <div key={e.cluster} className="flex items-start gap-2">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
              <span>
                <span className="font-medium">{e.cluster}</span>: {e.message}
              </span>
            </div>
          ))}
        </div>
      )}

      <div className="mt-6 border border-gray-200 bg-white">
        {clustersLoading ? (
          <p className="p-6 text-sm text-gray-500">Loading…</p>
        ) : clustersError ? (
          <p className="p-6 text-sm text-red-600">{clustersError}</p>
        ) : clusters.length === 0 ? (
          <NoClusters />
        ) : sortedRows.length === 0 && status !== "live" ? (
          <p className="p-6 text-sm text-gray-500">Connecting…</p>
        ) : sortedRows.length === 0 ? (
          <NoNamespaces skipped={skipped} />
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-400">
                <th className="px-4 py-2 font-medium">Namespace</th>
                <th className="px-4 py-2 font-medium">Cluster</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Age</th>
              </tr>
            </thead>
            <tbody>
              {sortedRows.map((n) => (
                <tr
                  key={`${n.clusterId}/${n.name}`}
                  onClick={() =>
                    navigate(
                      `/infrastructure/namespaces/${n.clusterId}/${encodeURIComponent(n.name)}`,
                    )
                  }
                  className="cursor-pointer border-b border-gray-100 last:border-0 hover:bg-gray-50"
                >
                  <td className="px-4 py-3 font-medium text-gray-900">
                    {n.name}
                  </td>
                  <td className="px-4 py-3 text-gray-600">{n.clusterName}</td>
                  <td className="px-4 py-3">
                    <NamespaceStatusBadge status={n.status} />
                  </td>
                  <td className="px-4 py-3 text-gray-600">
                    {nodeAge(n.created_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function LiveIndicator({ status }: { status: StreamStatus }) {
  const config: Record<
    StreamStatus,
    { label: string; dot: string; text: string; pulse: boolean }
  > = {
    live: { label: "Live", dot: "bg-green-500", text: "text-gray-600", pulse: false },
    connecting: {
      label: "Connecting…",
      dot: "bg-gray-400",
      text: "text-gray-500",
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
    <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${c.text}`}>
      <span
        className={`h-2 w-2 rounded-full ${c.dot} ${c.pulse ? "animate-pulse" : ""}`}
      />
      {c.label}
    </span>
  );
}

function NoClusters() {
  return (
    <div className="p-10 text-center">
      <Server className="mx-auto h-8 w-8 text-gray-300" />
      <p className="mt-3 text-sm font-medium text-gray-700">No clusters yet</p>
      <p className="mt-1 text-sm text-gray-500">
        Register a Kubernetes cluster to see its namespaces here.
      </p>
      <Link
        to="/providers/clusters"
        className="mt-4 inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800"
      >
        Go to Clusters
      </Link>
    </div>
  );
}

function NoNamespaces({ skipped }: { skipped: number }) {
  return (
    <div className="p-10 text-center">
      <Boxes className="mx-auto h-8 w-8 text-gray-300" />
      <p className="mt-3 text-sm font-medium text-gray-700">
        No namespaces to show
      </p>
      <p className="mt-1 text-sm text-gray-500">
        {skipped > 0
          ? `${skipped} cluster${skipped === 1 ? " is" : "s are"} not connected and ${skipped === 1 ? "was" : "were"} skipped — check its connection on the Clusters page.`
          : "This cluster reported no namespaces."}
      </p>
    </div>
  );
}

function NamespaceStatusBadge({ status }: { status: string }) {
  if (namespacePhase(status) === "Terminating") {
    return (
      <span className="inline-flex items-center gap-1 bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-800">
        <CircleSlash className="h-3.5 w-3.5" />
        Terminating
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
      Active
    </span>
  );
}
