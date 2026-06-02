import { useCallback, useEffect, useState } from "react";
import {
  CheckCircle2,
  CircleDashed,
  Plus,
  RefreshCw,
  Server,
  ShieldCheck,
  Trash2,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { ClusterCapabilitiesModal } from "../components/ClusterCapabilitiesModal";
import { RegisterClusterDialog } from "../components/RegisterClusterDialog";
import { CONNECTION_METHODS } from "../components/connectionMethods";

type Cluster = components["schemas"]["Cluster"];

// Clusters is the Providers › Clusters page: it lists the clusters registered
// to the current organization and opens a dialog to register more. It is the
// first org-scoped resource — the X-Organization-ID header is attached
// automatically by the API client (see api/client.ts).
export function Clusters() {
  const { currentOrg, currentRole } = useOrg();
  // Viewers can see clusters and their live status but take no action.
  const canEdit = currentRole !== "viewer";
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [registering, setRegistering] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [capsId, setCapsId] = useState<string | null>(null);
  // Clusters whose connectivity is being re-probed in the background.
  const [checking, setChecking] = useState<Set<string>>(new Set());

  // refreshConnectivity re-probes each cluster's live connectivity and updates
  // its row as results arrive. Replaces the old manual "Test" button: the user
  // shouldn't have to ask whether a cluster is still reachable — we check on
  // every load and reflect the current state in the status badge.
  const refreshConnectivity = useCallback(async (list: Cluster[]) => {
    if (list.length === 0) return;
    setChecking(new Set(list.map((c) => c.id)));
    await Promise.all(
      list.map(async (c) => {
        const { data } = await api.POST("/api/clusters/{id}/test", {
          params: { path: { id: c.id } },
        });
        if (data) {
          setClusters((cs) => cs.map((x) => (x.id === c.id ? data : x)));
        }
        setChecking((s) => {
          const next = new Set(s);
          next.delete(c.id);
          return next;
        });
      }),
    );
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/clusters");
    if (error) setError(error.message ?? "Could not load clusters");
    const list = data ?? [];
    setClusters(list);
    setLoading(false);
    void refreshConnectivity(list);
  }, [refreshConnectivity]);

  // Reload whenever the active organization changes.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function onDelete(id: string) {
    if (!confirm("Delete this cluster registration?")) return;
    setBusyId(id);
    const { error } = await api.DELETE("/api/clusters/{id}", {
      params: { path: { id } },
    });
    if (!error) setClusters((cs) => cs.filter((c) => c.id !== id));
    setBusyId(null);
  }

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Providers
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Clusters</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Register the Kubernetes clusters Spacefleet manages.
          </p>
        </div>
        {canEdit && (
          <button
            type="button"
            onClick={() => setRegistering(true)}
            className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
          >
            <Plus className="h-4 w-4" />
            Add cluster
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-neutral-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : clusters.length === 0 ? (
          <div className="p-10 text-center">
            <Server className="mx-auto h-8 w-8 text-neutral-300" />
            <p className="mt-3 text-sm font-medium text-neutral-700">
              No clusters yet
            </p>
            <p className="mt-1 text-sm text-neutral-500">
              Add your first cluster to start managing workloads.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Connection</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Version</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {clusters.map((c) => (
                <tr
                  key={c.id}
                  className="border-b border-neutral-100 last:border-0"
                >
                  <td className="px-4 py-3 font-medium text-neutral-900">
                    {c.name}
                    {c.endpoint && (
                      <span className="block text-xs font-normal text-neutral-400">
                        {c.endpoint}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {methodLabel(c.connection_method)}
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge
                      status={c.status}
                      message={c.status_message}
                      checking={checking.has(c.id)}
                    />
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {c.k8s_version || "—"}
                  </td>
                  <td className="px-4 py-3 text-right whitespace-nowrap">
                    <button
                      type="button"
                      onClick={() => setCapsId(c.id)}
                      className="inline-flex items-center gap-1.5 text-neutral-500 hover:text-neutral-900"
                    >
                      <ShieldCheck className="h-3.5 w-3.5" />
                      Capabilities
                    </button>
                    {canEdit && (
                      <button
                        type="button"
                        onClick={() => void onDelete(c.id)}
                        disabled={busyId === c.id}
                        className="ml-4 inline-flex items-center gap-1.5 text-red-500 hover:text-red-700 disabled:opacity-50"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {registering && (
        <RegisterClusterDialog
          onClose={() => setRegistering(false)}
          onRegistered={(c) => {
            setClusters((cs) => [...cs, c]);
            setRegistering(false);
            // Surface the capability report immediately so the operator can act
            // on any missing permissions right after connecting.
            setCapsId(c.id);
          }}
        />
      )}

      {capsId && (
        <ClusterCapabilitiesModal
          clusterId={capsId}
          clusterName={clusters.find((c) => c.id === capsId)?.name}
          onClose={() => setCapsId(null)}
        />
      )}
    </div>
  );
}

function methodLabel(method: Cluster["connection_method"]): string {
  return (
    CONNECTION_METHODS.find((m) => m.value === method)?.label ?? method
  );
}

function StatusBadge({
  status,
  message,
  checking,
}: {
  status: Cluster["status"];
  message?: string;
  // True while connectivity is being re-probed in the background.
  checking?: boolean;
}) {
  const styles: Record<Cluster["status"], string> = {
    connected: "bg-green-100 text-green-800",
    error: "bg-red-100 text-red-800",
    pending: "bg-neutral-100 text-neutral-700",
  };
  const Icon = {
    connected: CheckCircle2,
    error: XCircle,
    pending: CircleDashed,
  }[status];
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[status]}`}
      title={status === "error" ? message : undefined}
    >
      <Icon className="h-3.5 w-3.5" />
      {status}
      {checking && (
        <RefreshCw className="h-3 w-3 animate-spin opacity-60" aria-label="checking" />
      )}
    </span>
  );
}
