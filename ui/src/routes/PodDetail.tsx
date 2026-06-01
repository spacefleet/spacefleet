import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, FileText } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import {
  podAge,
  podStatusTone,
  type Cluster,
  type Pod,
  type StatusTone,
} from "../lib/pods";
import { useResourceStream } from "../lib/useResourceStream";
import { PodLogsModal } from "../components/PodLogsModal";

// PodDetail is the drill-down for a single pod, reached by clicking a row on the
// Pods page (route /infrastructure/pods/:clusterId/:namespace/:podName). It
// streams the cluster's pods live and renders the matching one, so the view
// stays current and is valid as a deep link / on refresh.
export function PodDetail() {
  const {
    clusterId = "",
    namespace = "",
    podName = "",
  } = useParams();
  const decodedNs = decodeURIComponent(namespace);
  const decodedName = decodeURIComponent(podName);
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const [cluster, setCluster] = useState<Cluster | null>(null);
  const [logsOpen, setLogsOpen] = useState(false);

  // The cluster (for its name) is fetched once; the pod itself streams live.
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

  const { items, status, error } = useResourceStream<Pod>(
    `/api/clusters/${clusterId}/pods/stream`,
    (p) => `${p.namespace}/${p.name}`,
  );
  const pod =
    items.find((p) => p.namespace === decodedNs && p.name === decodedName) ??
    null;

  const loading = !pod && !error && status !== "live";
  const displayError =
    error ??
    (!pod && status === "live"
      ? `Pod "${decodedName}" was not found in namespace "${decodedNs}".`
      : null);

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/infrastructure/pods")}
        className="inline-flex items-center gap-1.5 text-sm text-gray-500 hover:text-gray-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to pods
      </button>

      <div className="mt-3 flex items-start justify-between gap-4">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Infrastructure / Pods
            {cluster && <> / {cluster.name}</>} / {decodedNs}
          </p>
          <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
            {decodedName}
          </h1>
        </div>
        {pod && (
          <div className="flex shrink-0 items-center gap-3">
            <PodStatusBadge status={pod.status} ready={isReady(pod.ready)} />
            <button
              type="button"
              onClick={() => setLogsOpen(true)}
              className="inline-flex items-center gap-2 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-800"
            >
              <FileText className="h-4 w-4" />
              View logs
            </button>
          </div>
        )}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-gray-500">Loading…</p>
      ) : displayError ? (
        <div className="mt-6 border border-gray-200 bg-white p-10 text-center">
          <AlertTriangle className="mx-auto h-8 w-8 text-gray-300" />
          <p className="mt-3 text-sm font-medium text-gray-700">{displayError}</p>
          <Link
            to="/infrastructure/pods"
            className="mt-4 inline-block text-sm text-gray-600 underline hover:text-gray-900"
          >
            Return to pods
          </Link>
        </div>
      ) : pod ? (
        <div className="mt-6 space-y-6">
          <Section title="Overview">
            <Field label="Status" value={pod.status} />
            <Field label="Phase" value={pod.phase} />
            <Field label="Ready" value={pod.ready} />
            <Field label="Restarts" value={String(pod.restarts)} />
            <Field label="QoS class" value={pod.qos_class} />
            <Field label="Service account" value={pod.service_account} />
            <Field label="Age" value={podAge(pod.created_at)} />
            <Field
              label="Created"
              value={new Date(pod.created_at).toLocaleString()}
            />
          </Section>

          <Section title="Network">
            <Field label="Node" value={pod.node_name} />
            <Field label="Pod IP" value={pod.pod_ip} mono />
            <Field label="Host IP" value={pod.host_ip} mono />
          </Section>

          <ContainersPanel containers={pod.containers} />

          <ConditionsPanel conditions={pod.conditions} />

          <LabelsPanel labels={pod.labels} />
        </div>
      ) : null}

      {pod && logsOpen && (
        <PodLogsModal
          clusterId={clusterId}
          clusterName={cluster?.name}
          namespace={pod.namespace}
          podName={pod.name}
          containers={pod.containers.map((c) => c.name)}
          onClose={() => setLogsOpen(false)}
        />
      )}
    </div>
  );
}

// isReady reports whether every container is ready, from the "x/y" string.
function isReady(ready: string): boolean {
  const [r, t] = ready.split("/");
  return r !== undefined && r === t && t !== "0";
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
      <dt className="text-xs text-gray-400">{label}</dt>
      <dd
        className={`mt-0.5 break-all text-sm text-gray-900 ${mono ? "font-mono text-xs" : ""}`}
      >
        {value || "—"}
      </dd>
    </div>
  );
}

function ContainersPanel({ containers }: { containers: Pod["containers"] }) {
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        Containers
      </h2>
      {containers.length === 0 ? (
        <p className="p-4 text-sm text-gray-500">No container statuses reported.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs uppercase tracking-wide text-gray-400">
              <th className="px-4 py-2 font-medium">Container</th>
              <th className="px-4 py-2 font-medium">Image</th>
              <th className="px-4 py-2 font-medium">Ready</th>
              <th className="px-4 py-2 font-medium">State</th>
              <th className="px-4 py-2 font-medium">Restarts</th>
            </tr>
          </thead>
          <tbody>
            {containers.map((c) => (
              <tr key={c.name} className="border-b border-gray-100 last:border-0">
                <td className="px-4 py-2 font-medium text-gray-900">{c.name}</td>
                <td className="px-4 py-2 break-all font-mono text-xs text-gray-700">
                  {c.image || "—"}
                </td>
                <td className="px-4 py-2 text-gray-700">{c.ready ? "Yes" : "No"}</td>
                <td className="px-4 py-2 text-gray-700">
                  {c.state || "—"}
                  {c.state_reason ? ` (${c.state_reason})` : ""}
                </td>
                <td className="px-4 py-2 text-gray-700">{c.restart_count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function ConditionsPanel({ conditions }: { conditions: Pod["conditions"] }) {
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        Conditions
      </h2>
      {conditions.length === 0 ? (
        <p className="p-4 text-sm text-gray-500">No conditions reported.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-100 text-left text-xs uppercase tracking-wide text-gray-400">
              <th className="px-4 py-2 font-medium">Type</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Reason</th>
              <th className="px-4 py-2 font-medium">Message</th>
            </tr>
          </thead>
          <tbody>
            {conditions.map((c) => (
              <tr key={c.type} className="border-b border-gray-100 last:border-0">
                <td className="px-4 py-2 font-medium text-gray-900">{c.type}</td>
                <td className="px-4 py-2 text-gray-700">{c.status}</td>
                <td className="px-4 py-2 text-gray-600">{c.reason || "—"}</td>
                <td className="px-4 py-2 text-gray-600">{c.message || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
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
      className={`inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium ${TONE_CLASSES[tone]}`}
    >
      {status}
    </span>
  );
}
