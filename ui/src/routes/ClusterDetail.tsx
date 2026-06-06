import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  CircleDashed,
  RefreshCw,
  Trash2,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { ClusterCapabilities } from "../components/ClusterCapabilities";
import { TektonPanel } from "../components/TektonPanel";
import { CONNECTION_METHODS } from "../components/connectionMethods";

type Cluster = components["schemas"]["Cluster"];

// ClusterDetail is the single place to see and manage one registered cluster
// (route /admin/clusters/:clusterId). Registering a cluster lands here, and
// it's where the operator returns to re-check connectivity, review what the
// cluster's credentials can do and grant what's missing (Capabilities), and set
// up / run jobs (Tekton). The Clusters list is now just a way in — every
// per-cluster action lives on this page.
//
// On arrival it re-probes connectivity (the same check the list runs on load)
// so a freshly registered cluster shows its live status without a manual step,
// then keeps it current by re-probing on an interval — the operator never has
// to ask for a fresh result. The Capabilities report fetches itself on mount
// for the same reason.

// How often to re-probe live connectivity while the detail page is open.
const CONNECTIVITY_REFRESH_MS = 30_000;
export function ClusterDetail() {
  const { clusterId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  // Viewers can see a cluster and its status but take no action.
  const canEdit = currentRole !== "viewer";

  const [cluster, setCluster] = useState<Cluster | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // probing guards against overlapping /test calls: the endpoint mutates remote
  // state (a live connectivity probe), so a slow probe must not have another
  // fired on top of it by the interval or a concurrent load.
  const probing = useRef(false);

  // test re-probes connectivity and folds the refreshed row back into state. It
  // runs automatically on load and then on an interval (see below). It no-ops
  // while a probe is already in flight so the two callers can't overlap.
  const test = useCallback(async () => {
    if (probing.current) return;
    probing.current = true;
    setChecking(true);
    try {
      const { data } = await api.POST("/api/clusters/{id}/test", {
        params: { path: { id: clusterId } },
      });
      if (data) setCluster(data);
    } finally {
      probing.current = false;
      setChecking(false);
    }
  }, [clusterId]);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/clusters/{id}", {
      params: { path: { id: clusterId } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not load this cluster");
      setCluster(null);
      setLoading(false);
      return;
    }
    setCluster(data);
    setLoading(false);
    void test();
  }, [clusterId, test]);

  // Reload whenever the cluster or active organization changes.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Keep the connection status current while the page is open by re-probing on
  // an interval, so the badge reflects live reachability without a manual step.
  // Skip the tick while the tab is backgrounded — there's no one watching the
  // badge, and /test is a live remote probe we don't want firing unattended.
  // test() itself no-ops if a probe is still in flight, so the interval can't
  // pile probes on top of the on-load one.
  useEffect(() => {
    if (!clusterId) return;
    const id = setInterval(() => {
      if (document.hidden) return;
      void test();
    }, CONNECTIVITY_REFRESH_MS);
    return () => clearInterval(id);
  }, [clusterId, test]);

  async function onDelete() {
    if (!confirm("Delete this cluster registration?")) return;
    setDeleting(true);
    const { error } = await api.DELETE("/api/clusters/{id}", {
      params: { path: { id: clusterId } },
    });
    if (error) {
      setError(error.message ?? "Could not delete this cluster");
      setDeleting(false);
      return;
    }
    navigate("/admin/clusters");
  }

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/admin/clusters")}
        className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to clusters
      </button>

      <div className="mt-3 flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Admin / Clusters
          </p>
          <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
            {cluster?.name ?? (loading ? "…" : "Cluster")}
          </h1>
          {cluster?.endpoint && (
            <p className="mt-1 break-all font-mono text-xs text-neutral-400">
              {cluster.endpoint}
            </p>
          )}
        </div>
        {cluster && (
          <div className="flex shrink-0 items-center gap-3">
            <StatusBadge
              status={cluster.status}
              message={cluster.status_message}
              checking={checking}
            />
            {canEdit && (
              <button
                type="button"
                onClick={() => void onDelete()}
                disabled={deleting}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-red-600 hover:bg-red-50 disabled:opacity-50"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Delete
              </button>
            )}
          </div>
        )}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error || !cluster ? (
        <div className="mt-6 border border-neutral-200 bg-white p-10 text-center">
          <AlertTriangle className="mx-auto h-8 w-8 text-neutral-300" />
          <p className="mt-3 text-sm font-medium text-neutral-700">
            {error ?? "Cluster not found."}
          </p>
          <Link
            to="/admin/clusters"
            className="mt-4 inline-block text-sm text-neutral-600 underline hover:text-neutral-900"
          >
            Return to clusters
          </Link>
        </div>
      ) : (
        <div className="mt-6 space-y-6">
          <Overview cluster={cluster} />

          {/* All-in-one management view: Capabilities and Jobs sit side by side
              on wide screens and stack on narrow ones. Capabilities owns the
              full access report (including run_jobs), so the Jobs panel hides
              its embedded copy to avoid showing it twice. */}
          <div className="grid grid-cols-1 gap-6 xl:grid-cols-2 xl:items-start">
            <ClusterCapabilities clusterId={cluster.id} />
            <div className="border border-neutral-200 bg-white">
              <h2 className="border-b border-neutral-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-400">
                Jobs
              </h2>
              <TektonPanel
                clusterId={cluster.id}
                canEdit={canEdit}
                showCapabilities={false}
              />
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// Overview is the at-a-glance facts card: how the cluster is reached and what
// the last probe found.
function Overview({ cluster }: { cluster: Cluster }) {
  return (
    <div className="border border-neutral-200 bg-white">
      <h2 className="border-b border-neutral-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-400">
        Overview
      </h2>
      <dl className="grid grid-cols-1 gap-x-8 gap-y-3 p-4 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Connection" value={methodLabel(cluster.connection_method)} />
        <Field label="Status" value={cluster.status} />
        <Field label="Kubernetes version" value={cluster.k8s_version} />
        <Field label="Endpoint" value={cluster.endpoint} mono />
        <Field
          label="Last checked"
          value={
            cluster.last_checked_at
              ? new Date(cluster.last_checked_at).toLocaleString()
              : undefined
          }
        />
        <Field
          label="Registered"
          value={new Date(cluster.created_at).toLocaleString()}
        />
      </dl>
      {cluster.status === "error" && cluster.status_message && (
        <p className="border-t border-neutral-100 px-4 py-3 text-xs text-red-600">
          {cluster.status_message}
        </p>
      )}
    </div>
  );
}

function Field({
  label,
  value,
  mono,
}: {
  label: string;
  value?: string;
  mono?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd
        className={`mt-0.5 break-all text-sm text-neutral-900 ${mono ? "font-mono text-xs" : ""}`}
      >
        {value || "—"}
      </dd>
    </div>
  );
}

function methodLabel(method: Cluster["connection_method"]): string {
  return CONNECTION_METHODS.find((m) => m.value === method)?.label ?? method;
}

function StatusBadge({
  status,
  message,
  checking,
}: {
  status: Cluster["status"];
  message?: string;
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
      className={`inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium ${styles[status]}`}
      title={status === "error" ? message : undefined}
    >
      <Icon className="h-3.5 w-3.5" />
      {status}
      {checking && (
        <RefreshCw
          className="h-3 w-3 animate-spin opacity-60"
          aria-label="checking"
        />
      )}
    </span>
  );
}
