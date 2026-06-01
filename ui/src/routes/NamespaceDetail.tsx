import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, CircleSlash } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { namespacePhase, type Namespace } from "../lib/namespaces";
import { nodeAge, type Cluster } from "../lib/nodes";
import { useResourceStream } from "../lib/useResourceStream";

// NamespaceDetail is the drill-down for a single namespace, reached by clicking
// a row on the Namespaces page (route
// /infrastructure/namespaces/:clusterId/:namespaceName). It mirrors NodeDetail:
// the cluster's namespaces stream live and the matching one is rendered, so the
// view stays current and is valid as a deep link / on refresh.
export function NamespaceDetail() {
  const { clusterId = "", namespaceName = "" } = useParams();
  const decodedName = decodeURIComponent(namespaceName);
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const [cluster, setCluster] = useState<Cluster | null>(null);

  // The cluster (for its name) is fetched once; the namespace itself streams
  // live.
  useEffect(() => {
    let cancelled = false;
    void api
      .GET("/api/clusters/{id}", { params: { path: { id: clusterId } } })
      .then(({ data }) => {
        if (!cancelled) setCluster(data ?? null);
      });
    return () => {
      cancelled = true;
    };
  }, [clusterId, currentOrg?.id]);

  const { items, status, error } = useResourceStream<Namespace>(
    `/api/clusters/${clusterId}/namespaces/stream`,
    (n) => n.name,
  );
  const namespace = items.find((n) => n.name === decodedName) ?? null;

  const loading = !namespace && !error && status !== "live";
  const displayError =
    error ??
    (!namespace && status === "live"
      ? `Namespace "${decodedName}" was not found in this cluster.`
      : null);

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/infrastructure/namespaces")}
        className="inline-flex items-center gap-1.5 text-sm text-gray-500 hover:text-gray-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to namespaces
      </button>

      <div className="mt-3 flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Infrastructure / Namespaces
            {cluster && <> / {cluster.name}</>}
          </p>
          <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
            {decodedName}
          </h1>
        </div>
        {namespace && <NamespaceStatusBadge status={namespace.status} />}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-gray-500">Loading…</p>
      ) : displayError ? (
        <div className="mt-6 border border-gray-200 bg-white p-10 text-center">
          <AlertTriangle className="mx-auto h-8 w-8 text-gray-300" />
          <p className="mt-3 text-sm font-medium text-gray-700">{displayError}</p>
          <Link
            to="/infrastructure/namespaces"
            className="mt-4 inline-block text-sm text-gray-600 underline hover:text-gray-900"
          >
            Return to namespaces
          </Link>
        </div>
      ) : namespace ? (
        <div className="mt-6 space-y-6">
          <Section title="Overview">
            <Field
              label="Status"
              value={namespacePhase(namespace.status)}
            />
            <Field label="Age" value={nodeAge(namespace.created_at)} />
            <Field
              label="Created"
              value={new Date(namespace.created_at).toLocaleString()}
            />
          </Section>

          <LabelsPanel labels={namespace.labels} />
        </div>
      ) : null}
    </div>
  );
}

// Section is a titled card whose body is a responsive label/value grid.
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        {title}
      </h2>
      <dl className="grid grid-cols-1 gap-x-8 gap-y-3 p-4 sm:grid-cols-2 lg:grid-cols-3">
        {children}
      </dl>
    </div>
  );
}

function Field({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-xs text-gray-400">{label}</dt>
      <dd className="mt-0.5 break-all text-sm text-gray-900">{value || "—"}</dd>
    </div>
  );
}

function LabelsPanel({ labels }: { labels: Record<string, string> }) {
  const entries = Object.entries(labels).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        Labels
      </h2>
      {entries.length === 0 ? (
        <p className="p-4 text-sm text-gray-500">No labels.</p>
      ) : (
        <ul className="divide-y divide-gray-100">
          {entries.map(([k, v]) => (
            <li key={k} className="flex gap-2 px-4 py-2 font-mono text-xs">
              <span className="text-gray-500">{k}</span>
              <span className="text-gray-400">=</span>
              <span className="break-all text-gray-900">{v}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function NamespaceStatusBadge({ status }: { status: string }) {
  if (namespacePhase(status) === "Terminating") {
    return (
      <span className="inline-flex items-center gap-1 bg-amber-100 px-2.5 py-1 text-xs font-medium text-amber-800">
        <CircleSlash className="h-3.5 w-3.5" />
        Terminating
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 bg-green-100 px-2.5 py-1 text-xs font-medium text-green-800">
      Active
    </span>
  );
}
