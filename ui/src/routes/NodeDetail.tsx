import { useEffect, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import {
  AlertTriangle,
  CheckCircle2,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import { Breadcrumbs } from "../components/Breadcrumbs";
import { nodeAge, nodeRolesLabel, type Cluster, type Node } from "../lib/nodes";
import { useResourceStream } from "../lib/useResourceStream";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// NodeDetail is the drill-down for a single node, reached by clicking a row on
// the Nodes page (route /infrastructure/nodes/:clusterId/:nodeName). It streams
// the cluster's nodes live and renders every detail of the matching one, so the
// view stays current and is valid as a deep link / on refresh.
export function NodeDetail() {
  const { clusterId = "", nodeName = "" } = useParams();
  const decodedName = decodeURIComponent(nodeName);
  useDocumentTitle(decodedName, "Nodes");
  const { currentOrg } = useOrg();
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
      <Breadcrumbs
        items={[
          { label: "Infrastructure" },
          { label: "Nodes", to: "/infrastructure/nodes" },
          ...(cluster ? [{ label: cluster.name }] : []),
        ]}
      />

      <div className="mt-2 flex items-start justify-between">
        <div>
          <h1 className="break-all text-2xl font-bold tracking-tight">
            {decodedName}
          </h1>
        </div>
        {node && (
          <NodeStatusBadge ready={node.ready} unschedulable={node.unschedulable} />
        )}
      </div>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : displayError ? (
        <div className="mt-6 border border-neutral-800 bg-neutral-900 p-10 text-center">
          <AlertTriangle className="mx-auto h-8 w-8 text-neutral-600" />
          <p className="mt-3 text-sm font-medium text-neutral-300">{displayError}</p>
          <Link
            to="/infrastructure/nodes"
            className="mt-4 inline-block text-sm text-neutral-300 underline hover:text-neutral-100"
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
    <div className="border border-neutral-800 bg-neutral-900">
      <h2 className="border-b border-neutral-800 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-500">
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
      <dt className="text-xs text-neutral-500">{label}</dt>
      <dd
        className={`mt-0.5 break-all text-sm text-neutral-100 ${mono ? "font-mono text-xs" : ""}`}
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
    <div className="border border-neutral-800 bg-neutral-900">
      <h2 className="border-b border-neutral-800 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-500">
        Resources
      </h2>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
            <th className="px-4 py-2 font-medium">Resource</th>
            <th className="px-4 py-2 font-medium">Capacity</th>
            <th className="px-4 py-2 font-medium">Allocatable</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.label} className="border-b border-neutral-800 last:border-0">
              <td className="px-4 py-2 font-medium text-neutral-100">{r.label}</td>
              <td className="px-4 py-2 font-mono text-xs text-neutral-300">
                {r.cap || "—"}
              </td>
              <td className="px-4 py-2 font-mono text-xs text-neutral-300">
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
    <div className="border border-neutral-800 bg-neutral-900">
      <h2 className="border-b border-neutral-800 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-500">
        Conditions
      </h2>
      {conditions.length === 0 ? (
        <p className="p-4 text-sm text-neutral-400">No conditions reported.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
              <th className="px-4 py-2 font-medium">Type</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Reason</th>
              <th className="px-4 py-2 font-medium">Message</th>
            </tr>
          </thead>
          <tbody>
            {conditions.map((c) => (
              <tr key={c.type} className="border-b border-neutral-800 last:border-0">
                <td className="px-4 py-2 font-medium text-neutral-100">{c.type}</td>
                <td className="px-4 py-2 text-neutral-300">{c.status}</td>
                <td className="px-4 py-2 text-neutral-300">{c.reason || "—"}</td>
                <td className="px-4 py-2 text-neutral-300">{c.message || "—"}</td>
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
    <div className="border border-neutral-800 bg-neutral-900">
      <h2 className="border-b border-neutral-800 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-500">
        Taints
      </h2>
      {taints.length === 0 ? (
        <p className="p-4 text-sm text-neutral-400">No taints.</p>
      ) : (
        <ul className="divide-y divide-neutral-800">
          {taints.map((t) => (
            <li
              key={`${t.key}:${t.effect}`}
              className="px-4 py-2 font-mono text-xs text-neutral-300"
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
    <div className="border border-neutral-800 bg-neutral-900">
      <h2 className="border-b border-neutral-800 px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-500">
        Labels
      </h2>
      {entries.length === 0 ? (
        <p className="p-4 text-sm text-neutral-400">No labels.</p>
      ) : (
        <ul className="divide-y divide-neutral-800">
          {entries.map(([k, v]) => (
            <li key={k} className="flex gap-2 px-4 py-2 font-mono text-xs">
              <span className="text-neutral-400">{k}</span>
              <span className="text-neutral-500">=</span>
              <span className="break-all text-neutral-100">{v}</span>
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
      <span className="inline-flex items-center gap-1 bg-red-500/15 px-2.5 py-1 text-xs font-medium text-red-300">
        <XCircle className="h-3.5 w-3.5" />
        NotReady
      </span>
    );
  }
  if (unschedulable) {
    return (
      <span className="inline-flex items-center gap-1 bg-amber-500/15 px-2.5 py-1 text-xs font-medium text-amber-300">
        <AlertTriangle className="h-3.5 w-3.5" />
        Ready,SchedulingDisabled
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 bg-green-500/15 px-2.5 py-1 text-xs font-medium text-green-300">
      <CheckCircle2 className="h-3.5 w-3.5" />
      Ready
    </span>
  );
}
