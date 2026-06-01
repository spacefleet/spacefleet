import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AlertTriangle, Boxes, FileText, MoreVertical, Server } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import {
  ALL,
  podAge,
  podStatusTone,
  type Cluster,
  type PodRow,
  type StatusTone,
} from "../lib/pods";
import { useClusterPodStreams } from "../lib/useClusterPodStreams";
import { PodLogsModal } from "../components/PodLogsModal";
import type { StreamStatus } from "../lib/resourceStream";

// Pods is the Infrastructure › Pods page. It lists the Kubernetes pods of the
// organization's registered clusters and updates live, with two coordinated
// filters: cluster and namespace. Every connected cluster is streamed (each
// stream carries all namespaces), so both filters are instant client-side
// projections over one dataset — and they stay in sync: choosing a cluster
// narrows the namespace list to that cluster's namespaces, and choosing a
// namespace narrows the cluster list to the clusters that have it. A selection
// that becomes irrelevant resets to "all".
export function Pods() {
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [clustersLoading, setClustersLoading] = useState(true);
  const [clustersError, setClustersError] = useState<string | null>(null);
  const [clusterFilter, setClusterFilter] = useState<string>(ALL);
  const [namespaceFilter, setNamespaceFilter] = useState<string>(ALL);
  // The pod whose logs are open in the modal, if any.
  const [logsPod, setLogsPod] = useState<PodRow | null>(null);

  // The cluster list itself is fetched once (it changes rarely, via the
  // Clusters page); only the pods within them stream live.
  const loadClusters = useCallback(async () => {
    setClustersLoading(true);
    setClustersError(null);
    const { data, error } = await api.GET("/api/clusters");
    if (error) setClustersError(error.message ?? "Could not load clusters");
    setClusters(data ?? []);
    setClusterFilter(ALL);
    setNamespaceFilter(ALL);
    setClustersLoading(false);
  }, []);

  useEffect(() => {
    void loadClusters();
  }, [loadClusters, currentOrg?.id]);

  // Only connected clusters can serve pods; we stream all of them so namespace
  // selection can narrow clusters (and vice versa) over a complete dataset.
  const connected = useMemo(
    () => clusters.filter((c) => c.status === "connected"),
    [clusters],
  );

  const { rows, status, errors } = useClusterPodStreams(connected);

  // Namespaces available for the current cluster selection.
  const namespaces = useMemo(() => {
    const set = new Set<string>();
    for (const r of rows) {
      if (clusterFilter === ALL || r.clusterId === clusterFilter)
        set.add(r.namespace);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [rows, clusterFilter]);

  // Clusters available for the current namespace selection ("vice versa").
  const clusterChoices = useMemo(() => {
    if (namespaceFilter === ALL) return connected;
    const ids = new Set(
      rows.filter((r) => r.namespace === namespaceFilter).map((r) => r.clusterId),
    );
    return connected.filter((c) => ids.has(c.id));
  }, [connected, rows, namespaceFilter]);

  // Keep each filter valid as the other filter (and the live data) changes.
  // Resetting to ALL only widens the other's options, so the two guards
  // converge rather than oscillate.
  useEffect(() => {
    if (namespaceFilter !== ALL && !namespaces.includes(namespaceFilter))
      setNamespaceFilter(ALL);
  }, [namespaces, namespaceFilter]);

  useEffect(() => {
    if (
      clusterFilter !== ALL &&
      !clusterChoices.some((c) => c.id === clusterFilter)
    )
      setClusterFilter(ALL);
  }, [clusterChoices, clusterFilter]);

  const visibleRows = useMemo(
    () =>
      rows
        .filter(
          (r) =>
            (clusterFilter === ALL || r.clusterId === clusterFilter) &&
            (namespaceFilter === ALL || r.namespace === namespaceFilter),
        )
        .sort(
          (a, b) =>
            a.clusterName.localeCompare(b.clusterName) ||
            a.namespace.localeCompare(b.namespace) ||
            a.name.localeCompare(b.name),
        ),
    [rows, clusterFilter, namespaceFilter],
  );

  // Connected clusters are the universe we stream; clusters that exist but
  // aren't connected are skipped and surfaced so an empty table isn't mistaken
  // for "no pods".
  const skipped = clusters.length - connected.length;

  return (
    <div>
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Infrastructure
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Pods</h1>
          <p className="mt-1 text-sm text-gray-600">
            The Kubernetes pods across your registered clusters, updated live.
          </p>
        </div>
        {!clustersLoading && clusters.length > 0 && (
          <div className="flex flex-wrap items-center justify-end gap-3">
            {connected.length > 0 && <LiveIndicator status={status} />}
            <label className="flex items-center gap-2 text-sm text-gray-600">
              Cluster
              <select
                value={clusterFilter}
                onChange={(e) => setClusterFilter(e.target.value)}
                className="border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-900 focus:border-black focus:outline-none"
              >
                <option value={ALL}>All clusters</option>
                {clusterChoices.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex items-center gap-2 text-sm text-gray-600">
              Namespace
              <select
                value={namespaceFilter}
                onChange={(e) => setNamespaceFilter(e.target.value)}
                className="border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-900 focus:border-black focus:outline-none"
              >
                <option value={ALL}>All namespaces</option>
                {namespaces.map((ns) => (
                  <option key={ns} value={ns}>
                    {ns}
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
        ) : visibleRows.length === 0 && status !== "live" ? (
          <p className="p-6 text-sm text-gray-500">Connecting…</p>
        ) : visibleRows.length === 0 ? (
          <NoPods skipped={skipped} filtered={namespaceFilter !== ALL || clusterFilter !== ALL} />
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-400">
                <th className="px-4 py-2 font-medium">Pod</th>
                <th className="px-4 py-2 font-medium">Namespace</th>
                <th className="px-4 py-2 font-medium">Cluster</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Ready</th>
                <th className="px-4 py-2 font-medium">Restarts</th>
                <th className="px-4 py-2 font-medium">Node</th>
                <th className="px-4 py-2 font-medium">Age</th>
                <th className="w-10 px-4 py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {visibleRows.map((p) => (
                <tr
                  key={`${p.clusterId}/${p.namespace}/${p.name}`}
                  onClick={() =>
                    navigate(
                      `/infrastructure/pods/${p.clusterId}/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.name)}`,
                    )
                  }
                  className="cursor-pointer border-b border-gray-100 last:border-0 hover:bg-gray-50"
                >
                  <td className="px-4 py-3 font-medium text-gray-900">
                    {p.name}
                    {p.pod_ip && (
                      <span className="block text-xs font-normal text-gray-400">
                        {p.pod_ip}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-gray-600">{p.namespace}</td>
                  <td className="px-4 py-3 text-gray-600">{p.clusterName}</td>
                  <td className="px-4 py-3">
                    <PodStatusBadge status={p.status} ready={isReady(p.ready)} />
                  </td>
                  <td className="px-4 py-3 text-gray-600">{p.ready}</td>
                  <td className="px-4 py-3 text-gray-600">{p.restarts}</td>
                  <td className="px-4 py-3 text-gray-600">{p.node_name || "—"}</td>
                  <td className="px-4 py-3 text-gray-600">{podAge(p.created_at)}</td>
                  <td
                    className="px-4 py-3"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <RowActions onViewLogs={() => setLogsPod(p)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {logsPod && (
        <PodLogsModal
          clusterId={logsPod.clusterId}
          clusterName={logsPod.clusterName}
          namespace={logsPod.namespace}
          podName={logsPod.name}
          containers={logsPod.containers.map((c) => c.name)}
          onClose={() => setLogsPod(null)}
        />
      )}
    </div>
  );
}

// RowActions is the per-row kebab menu: a vertical 3-dot button that opens a
// small action menu (currently just "View logs"). It manages its own open state
// and closes on an outside click; clicks are stopped from bubbling so they
// don't trigger the row's navigate-to-detail handler.
function RowActions({ onViewLogs }: { onViewLogs: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);

  return (
    <div ref={ref} className="relative flex justify-end">
      <button
        type="button"
        aria-label="Pod actions"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        className="p-1 text-gray-400 hover:text-gray-900"
      >
        <MoreVertical className="h-4 w-4" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-10 mt-1 min-w-[9rem] border border-gray-200 bg-white py-1 shadow-lg">
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              setOpen(false);
              onViewLogs();
            }}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-gray-700 hover:bg-gray-50"
          >
            <FileText className="h-4 w-4" />
            View logs
          </button>
        </div>
      )}
    </div>
  );
}

// isReady reports whether every container is ready, from the "x/y" string.
function isReady(ready: string): boolean {
  const [r, t] = ready.split("/");
  return r !== undefined && r === t && t !== "0";
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
        Register a Kubernetes cluster to see its pods here.
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

function NoPods({ skipped, filtered }: { skipped: number; filtered: boolean }) {
  return (
    <div className="p-10 text-center">
      <Boxes className="mx-auto h-8 w-8 text-gray-300" />
      <p className="mt-3 text-sm font-medium text-gray-700">No pods to show</p>
      <p className="mt-1 text-sm text-gray-500">
        {filtered
          ? "No pods match the selected filters."
          : skipped > 0
            ? `${skipped} cluster${skipped === 1 ? " is" : "s are"} not connected and ${skipped === 1 ? "was" : "were"} skipped — check its connection on the Clusters page.`
            : "These clusters reported no pods."}
      </p>
    </div>
  );
}

const TONE_CLASSES: Record<StatusTone, string> = {
  good: "bg-green-100 text-green-800",
  bad: "bg-red-100 text-red-800",
  warn: "bg-amber-100 text-amber-800",
  neutral: "bg-gray-100 text-gray-700",
};

function PodStatusBadge({ status, ready }: { status: string; ready: boolean }) {
  const tone = podStatusTone(status, ready);
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${TONE_CLASSES[tone]}`}
    >
      {status}
    </span>
  );
}
