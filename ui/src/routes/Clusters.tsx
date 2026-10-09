import { useCallback, useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import {
  CheckCircle2,
  CircleDashed,
  Play,
  Plus,
  RefreshCw,
  Server,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { RegisterClusterDialog } from "../components/RegisterClusterDialog";
import { CONNECTION_METHODS } from "../components/connectionMethods";
import { useDocumentTitle } from "../lib/useDocumentTitle";

type Cluster = components["schemas"]["Cluster"];

// Clusters is the Admin › Clusters page: it lists the clusters registered
// to the current organization and opens a dialog to register more. Each row is
// a way into the cluster's detail page (/admin/clusters/:id) — that's where
// every per-cluster action now lives (capabilities, runner, delete). The
// list itself just shows identity and live connection status. Linking here
// with ?register=1 opens the dialog on arrival. It is the first
// org-scoped resource — the X-Organization-ID header is attached automatically
// by the API client (see api/client.ts).
export function Clusters() {
  const { currentOrg, currentRole } = useOrg();
  useDocumentTitle("Clusters");
  // Viewers can see clusters and their live status but take no action.
  const canEdit = currentRole !== "viewer";
  const navigate = useNavigate();
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [registering, setRegistering] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  // Clusters whose connectivity is being re-probed in the background.
  const [checking, setChecking] = useState<Set<string>>(new Set());

  // refreshConnectivity re-probes each cluster's live connectivity and updates
  // its row as results arrive. The user shouldn't have to ask whether a cluster
  // is still reachable — we check on every load and reflect the current state in
  // the status badge.
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

  // ?register=1 (e.g. from the "Register a cluster" link on Applications) opens
  // the dialog, then drops the param so closing the dialog or refreshing the
  // page doesn't reopen it.
  useEffect(() => {
    if (!searchParams.has("register")) return;
    if (canEdit) setRegistering(true);
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("register");
        return next;
      },
      { replace: true },
    );
  }, [searchParams, setSearchParams, canEdit]);

  // Reload whenever the active organization changes.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-500">
            Admin
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Clusters</h1>
          <p className="mt-1 text-sm text-neutral-300">
            Register the Kubernetes clusters Spacefleet runs workloads on.
          </p>
        </div>
        {canEdit && (
          <button
            type="button"
            onClick={() => setRegistering(true)}
            className="inline-flex shrink-0 items-center gap-2 bg-primary px-4 py-2 text-sm font-medium text-primary-fg hover:bg-primary-hover"
          >
            <Plus className="h-4 w-4" />
            Add cluster
          </button>
        )}
      </div>

      <div className="mt-6 border border-neutral-800 bg-neutral-900">
        {loading ? (
          <p className="p-6 text-sm text-neutral-400">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-400">{error}</p>
        ) : clusters.length === 0 ? (
          <div className="p-10 text-center">
            <Server className="mx-auto h-8 w-8 text-neutral-600" />
            <p className="mt-3 text-sm font-medium text-neutral-300">
              No clusters yet
            </p>
            <p className="mt-1 text-sm text-neutral-400">
              Add your first cluster to start managing workloads.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Connection</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Runner</th>
                <th className="px-4 py-2 font-medium">Version</th>
              </tr>
            </thead>
            <tbody>
              {clusters.map((c) => (
                <tr
                  key={c.id}
                  onClick={() => navigate(`/admin/clusters/${c.id}`)}
                  className="cursor-pointer border-b border-neutral-800 last:border-0 hover:bg-neutral-800"
                >
                  <td className="px-4 py-3 font-medium text-neutral-100">
                    {c.name}
                    {c.endpoint && (
                      <span className="block text-xs font-normal text-neutral-500">
                        {c.endpoint}
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-neutral-300">
                    {methodLabel(c.connection_method)}
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge
                      status={c.status}
                      message={c.status_message}
                      checking={checking.has(c.id)}
                    />
                  </td>
                  <td className="px-4 py-3">
                    <RunnerBadge runsJobs={c.runs_jobs} />
                  </td>
                  <td className="px-4 py-3 text-neutral-300">
                    {c.k8s_version || "—"}
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
            setRegistering(false);
            // Land on the new cluster's detail page, where its capability check
            // runs on arrival so the operator can act on anything missing right
            // after connecting.
            navigate(`/admin/clusters/${c.id}`);
          }}
        />
      )}
    </div>
  );
}

// RunnerBadge shows whether a cluster is set up as a runner (applications can
// run their workflow jobs on it). This is plain info, not a connection state —
// so it reads as understated neutral text (with a small icon) rather than a
// colored status chip like the connection badge, which would otherwise be easy
// to confuse with it. Under the "Runner" column it says "Enabled" (the cluster
// is eligible to run jobs, not necessarily running one right now) and shows
// nothing at all when it isn't a runner.
function RunnerBadge({ runsJobs }: { runsJobs: boolean }) {
  if (!runsJobs) {
    return null;
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-neutral-300">
      <Play className="h-3.5 w-3.5 text-neutral-500" />
      Enabled
    </span>
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
    connected: "bg-green-500/15 text-green-300",
    error: "bg-red-500/15 text-red-300",
    pending: "bg-neutral-800 text-neutral-300",
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
