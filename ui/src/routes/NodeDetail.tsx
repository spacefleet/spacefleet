import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { nodeAge, nodeRolesLabel, type Cluster, type Node } from "../lib/nodes";
import { useResourceStream } from "../lib/useResourceStream";

// NodeDetail is the drill-down for a single node, reached by clicking a row on
// the Nodes page (route /infrastructure/nodes/:clusterId/:nodeName). It streams
// the cluster's nodes live and renders every detail of the matching one, so the
// view stays current and is valid as a deep link / on refresh.
export function NodeDetail() {
  const { clusterId = "", nodeName = "" } = useParams();
  const decodedName = decodeURIComponent(nodeName);
  const { currentOrg } = useOrg();
  const navigate = useNavigate();
  const [cluster, setCluster] = useState<Cluster | null>(null);

  // The cluster (for its name) is fetched once; the node itself streams live.
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

  const { items, status, error } = useResourceStream<Node>(
    `/api/clusters/${clusterId}/nodes/stream`,
    (n) => n.name,
  );
  const node = items.find((n) => n.name === decodedName) ?? null;

  const loading = !node && !error && status !== "live";
  const displayError =
    error ??
    (!node && status === "live"
      ? `Node "${decodedName}" was not found in this cluster.`
      : null);

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate("/infrastructure/nodes")}
        className="inline-flex items-center gap-1.5 text-sm text-gray-500 hover:text-gray-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to nodes
      </button>

      <div className="mt-3 flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
            Infrastructure / Nodes
            {cluster && <> / {cluster.name}</>}
          </p>
          <h1 className="mt-1 break-all text-2xl font-bold tracking-tight">
            {decodedName}
          </h1>
        </div>
        {node && (
          <NodeStatusBadge ready={node.ready} unschedulable={node.unschedulable} />
        )}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-gray-500">Loading…</p>
      ) : displayError ? (
        <div className="mt-6 border border-gray-200 bg-white p-10 text-center">
          <AlertTriangle className="mx-auto h-8 w-8 text-gray-300" />
          <p className="mt-3 text-sm font-medium text-gray-700">{displayError}</p>
          <Link
            to="/infrastructure/nodes"
            className="mt-4 inline-block text-sm text-gray-600 underline hover:text-gray-900"
          >
            Return to nodes
          </Link>
        </div>
      ) : node ? (
        <div className="mt-6 space-y-6">
          <Section title="Overview">
            <Field label="Status" value={node.ready ? "Ready" : "NotReady"} />
            <Field
              label="Schedulable"
              value={node.unschedulable ? "No (cordoned)" : "Yes"}
            />
            <Field label="Roles" value={nodeRolesLabel(node.roles)} />
            <Field label="Age" value={nodeAge(node.created_at)} />
            <Field
              label="Created"
              value={new Date(node.created_at).toLocaleString()}
            />
            <Field label="Provider ID" value={node.provider_id} mono />
          </Section>

          <Section title="System">
            <Field label="Kubelet version" value={node.kubelet_version} />
            <Field label="Container runtime" value={node.container_runtime} />
            <Field label="OS image" value={node.os_image} />
            <Field label="Kernel version" value={node.kernel_version} />
            <Field label="Operating system" value={node.operating_system} />
            <Field label="Architecture" value={node.architecture} />
          </Section>

          <Section title="Network">
            <Field label="Internal IP" value={node.internal_ip} mono />
            <Field label="External IP" value={node.external_ip} mono />
            <Field label="Hostname" value={node.hostname} mono />
            <Field label="Pod CIDR" value={node.pod_cidr} mono />
          </Section>

          <Section title="Placement">
            <Field label="Instance type" value={node.instance_type} />
            <Field label="Zone" value={node.zone} />
            <Field label="Region" value={node.region} />
          </Section>

          <ResourcesPanel
            capacity={node.capacity}
            allocatable={node.allocatable}
          />

          <ConditionsPanel conditions={node.conditions} />

          <TaintsPanel taints={node.taints} />

          <LabelsPanel labels={node.labels} />
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

function ResourcesPanel({
  capacity,
  allocatable,
}: {
  capacity?: Node["capacity"];
  allocatable?: Node["allocatable"];
}) {
  const rows: { label: string; cap?: string; alloc?: string }[] = [
    { label: "CPU", cap: capacity?.cpu, alloc: allocatable?.cpu },
    { label: "Memory", cap: capacity?.memory, alloc: allocatable?.memory },
    { label: "Pods", cap: capacity?.pods, alloc: allocatable?.pods },
  ];
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        Resources
      </h2>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-gray-100 text-left text-xs uppercase tracking-wide text-gray-400">
            <th className="px-4 py-2 font-medium">Resource</th>
            <th className="px-4 py-2 font-medium">Capacity</th>
            <th className="px-4 py-2 font-medium">Allocatable</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.label} className="border-b border-gray-100 last:border-0">
              <td className="px-4 py-2 font-medium text-gray-900">{r.label}</td>
              <td className="px-4 py-2 font-mono text-xs text-gray-700">
                {r.cap || "—"}
              </td>
              <td className="px-4 py-2 font-mono text-xs text-gray-700">
                {r.alloc || "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ConditionsPanel({ conditions }: { conditions: Node["conditions"] }) {
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

function TaintsPanel({ taints }: { taints: Node["taints"] }) {
  return (
    <div className="border border-gray-200 bg-white">
      <h2 className="border-b border-gray-200 px-4 py-2 text-xs font-medium uppercase tracking-wide text-gray-400">
        Taints
      </h2>
      {taints.length === 0 ? (
        <p className="p-4 text-sm text-gray-500">No taints.</p>
      ) : (
        <ul className="divide-y divide-gray-100">
          {taints.map((t) => (
            <li
              key={`${t.key}:${t.effect}`}
              className="px-4 py-2 font-mono text-xs text-gray-700"
            >
              {t.key}
              {t.value ? `=${t.value}` : ""}:{t.effect}
            </li>
          ))}
        </ul>
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

function NodeStatusBadge({
  ready,
  unschedulable,
}: {
  ready: boolean;
  unschedulable: boolean;
}) {
  if (!ready) {
    return (
      <span className="inline-flex items-center gap-1 bg-red-100 px-2.5 py-1 text-xs font-medium text-red-800">
        <XCircle className="h-3.5 w-3.5" />
        NotReady
      </span>
    );
  }
  if (unschedulable) {
    return (
      <span className="inline-flex items-center gap-1 bg-amber-100 px-2.5 py-1 text-xs font-medium text-amber-800">
        <AlertTriangle className="h-3.5 w-3.5" />
        Ready,SchedulingDisabled
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 bg-green-100 px-2.5 py-1 text-xs font-medium text-green-800">
      <CheckCircle2 className="h-3.5 w-3.5" />
      Ready
    </span>
  );
}
