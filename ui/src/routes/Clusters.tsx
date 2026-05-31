import { useCallback, useEffect, useState } from "react";
import {
  CheckCircle2,
  CircleDashed,
  Plus,
  RefreshCw,
  Server,
  Trash2,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import {
  RegisterClusterDialog,
  CONNECTION_METHODS,
} from "../components/RegisterClusterDialog";

type Cluster = components["schemas"]["Cluster"];

// Clusters is the Providers › Clusters page: it lists the clusters registered
// to the current organization and opens a dialog to register more. It is the
// first org-scoped resource — the X-Organization-ID header is attached
// automatically by the API client (see api/client.ts).
export function Clusters() {
  const { currentOrg } = useOrg();
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [registering, setRegistering] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/clusters");
    if (error) setError(error.message ?? "Could not load clusters");
    setClusters(data ?? []);
    setLoading(false);
  }, []);

  // Reload whenever the active organization changes.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  async function onTest(id: string) {
    setBusyId(id);
    const { data } = await api.POST("/api/clusters/{id}/test", {
      params: { path: { id } },
    });
    if (data) setClusters((cs) => cs.map((c) => (c.id === id ? data : c)));
    setBusyId(null);
  }

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
    <div className="mx-auto max-w-5xl">
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Providers
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Clusters</h1>
          <p className="mt-1 text-sm text-gray-600">
            Register the Kubernetes clusters Spacefleet manages.
          </p>
        </div>
        <button
          type="button"
          onClick={() => setRegistering(true)}
          className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-gray-800"
        >
          <Plus className="h-4 w-4" />
          Add cluster
        </button>
      </div>

      <div className="mt-6 border border-gray-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-gray-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : clusters.length === 0 ? (
          <div className="p-10 text-center">
            <Server className="mx-auto h-8 w-8 text-gray-300" />
            <p className="mt-3 text-sm font-medium text-gray-700">
              No clusters yet
            </p>
            <p className="mt-1 text-sm text-gray-500">
              Add your first cluster to start managing workloads.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Connection</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Version</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {clusters.map((c) => (
                <tr key={c.id} className="border-b border-gray-100 last:border-0">
                  <td className="px-4 py-3 font-medium text-gray-900">
                    {c.name}
                    {c.endpoint && (
                      <span className="block text-xs font-normal text-gray-400">
                        {c.endpoint}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-gray-600">
                    {methodLabel(c.connection_method)}
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={c.status} message={c.status_message} />
                  </td>
                  <td className="px-4 py-3 text-gray-600">
                    {c.k8s_version || "—"}
                  </td>
                  <td className="px-4 py-3 text-right whitespace-nowrap">
                    <button
                      type="button"
                      onClick={() => void onTest(c.id)}
                      disabled={busyId === c.id}
                      className="inline-flex items-center gap-1.5 text-gray-500 hover:text-gray-900 disabled:opacity-50"
                    >
                      <RefreshCw
                        className={`h-3.5 w-3.5 ${busyId === c.id ? "animate-spin" : ""}`}
                      />
                      Test
                    </button>
                    <button
                      type="button"
                      onClick={() => void onDelete(c.id)}
                      disabled={busyId === c.id}
                      className="ml-4 inline-flex items-center gap-1.5 text-red-500 hover:text-red-700 disabled:opacity-50"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                      Delete
                    </button>
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
          }}
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
}: {
  status: Cluster["status"];
  message?: string;
}) {
  const styles: Record<Cluster["status"], string> = {
    connected: "bg-green-100 text-green-800",
    error: "bg-red-100 text-red-800",
    pending: "bg-gray-100 text-gray-700",
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
    </span>
  );
}
