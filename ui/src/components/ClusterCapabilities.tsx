import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Copy,
  RefreshCw,
  XCircle,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type ClusterCapabilities = components["schemas"]["ClusterCapabilities"];
type Capability = components["schemas"]["Capability"];
type CapabilityRule = components["schemas"]["CapabilityRule"];

// ClusterCapabilities renders a live "Capabilities / Access" report for one
// registered cluster: it asks the API which product capabilities the cluster's
// stored credentials are actually allowed (a one-shot GET that probes the
// Kubernetes API), then shows each capability — grouped by area — as
// allowed/denied. A denied capability expands to list the RBAC rules it is
// missing. To enable capabilities, check one or more and use "Generate RBAC" at
// the bottom: the API returns a single ClusterRole + ClusterRoleBinding granting
// the selection's full rule set, bound to the identity these credentials map to.
//
// The X-Organization-ID header is attached automatically by the API client, so
// the report is always scoped to the active organization.
export function ClusterCapabilities({
  clusterId,
  bordered = true,
}: {
  clusterId: string;
  // When false, drop the outer border so the report sits flush inside a
  // container that already has one (e.g. the capabilities modal).
  bordered?: boolean;
}) {
  const { currentOrg } = useOrg();
  const [report, setReport] = useState<ClusterCapabilities | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/clusters/{id}/capabilities", {
      params: { path: { id: clusterId } },
    });
    if (error) {
      setError(error.message ?? "Could not check capabilities");
      setReport(null);
    } else {
      setReport(data ?? null);
    }
    setLoading(false);
  }, [clusterId]);

  // Re-check whenever the cluster or active organization changes.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  return (
    <div className={bordered ? "border border-neutral-200 bg-white" : "bg-white"}>
      <div className="flex items-center justify-between border-b border-neutral-200 px-4 py-2">
        <h2 className="text-xs font-medium uppercase tracking-wide text-neutral-400">
          Capabilities / Access
        </h2>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          className="inline-flex items-center gap-1.5 text-xs text-neutral-500 hover:text-neutral-900 disabled:opacity-50"
        >
          <RefreshCw className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`} />
          Re-check
        </button>
      </div>

      {loading ? (
        <p className="p-4 text-sm text-neutral-500">Checking access…</p>
      ) : error ? (
        <p className="p-4 text-sm text-red-600">{error}</p>
      ) : !report ? (
        <p className="p-4 text-sm text-neutral-500">No capability report.</p>
      ) : (
        <CapabilityReport clusterId={clusterId} report={report} />
      )}
    </div>
  );
}

function CapabilityReport({
  clusterId,
  report,
}: {
  clusterId: string;
  report: ClusterCapabilities;
}) {
  const groups = groupByArea(report.capabilities);
  // The set of capability keys the operator has checked to include in the
  // generated grant. Allowed and denied capabilities are both selectable: the
  // grant is the full rule set of the selection, so it stands alone regardless
  // of what is already allowed.
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const toggle = useCallback((key: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  return (
    <div>
      <IdentityLine identity={report.identity} />
      {groups.length === 0 ? (
        <p className="p-4 text-sm text-neutral-500">
          No capabilities reported.
        </p>
      ) : (
        <>
          {groups.map(([area, caps]) => (
            <AreaGroup
              key={area}
              area={area}
              capabilities={caps}
              selected={selected}
              onToggle={toggle}
            />
          ))}
          <GenerateRbac clusterId={clusterId} selected={selected} />
        </>
      )}
    </div>
  );
}

function IdentityLine({
  identity,
}: {
  identity: ClusterCapabilities["identity"];
}) {
  const subject = identity.username || "(unknown subject)";
  return (
    <p className="border-b border-neutral-100 px-4 py-2 text-xs text-neutral-500">
      Resolved identity{" "}
      <span className="font-mono text-neutral-700">{subject}</span>
      {identity.groups.length > 0 && (
        <span className="text-neutral-400">
          {" "}
          · groups {identity.groups.join(", ")}
        </span>
      )}
    </p>
  );
}

function AreaGroup({
  area,
  capabilities,
  selected,
  onToggle,
}: {
  area: string;
  capabilities: Capability[];
  selected: Set<string>;
  onToggle: (key: string) => void;
}) {
  return (
    <div className="border-b border-neutral-100 last:border-0">
      <h3 className="px-4 pt-3 pb-1 text-[11px] font-medium uppercase tracking-wide text-neutral-400">
        {area}
      </h3>
      <ul className="divide-y divide-neutral-100">
        {capabilities.map((cap) => (
          <CapabilityRow
            key={cap.key}
            capability={cap}
            checked={selected.has(cap.key)}
            onToggle={() => onToggle(cap.key)}
          />
        ))}
      </ul>
    </div>
  );
}

function CapabilityRow({
  capability,
  checked,
  onToggle,
}: {
  capability: Capability;
  checked: boolean;
  onToggle: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const denied = capability.status === "denied";

  return (
    <li>
      <div className="flex items-center justify-between px-4 py-2">
        <div className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={checked}
            onChange={onToggle}
            aria-label={`Include ${capability.title}`}
            className="h-4 w-4 shrink-0 accent-neutral-900"
          />
          {denied ? (
            <button
              type="button"
              onClick={() => setExpanded((e) => !e)}
              aria-expanded={expanded}
              className="inline-flex items-center gap-1.5 text-sm text-neutral-900 hover:text-neutral-600"
            >
              {expanded ? (
                <ChevronDown className="h-4 w-4 text-neutral-400" />
              ) : (
                <ChevronRight className="h-4 w-4 text-neutral-400" />
              )}
              {capability.title}
            </button>
          ) : (
            <span className="pl-[1.375rem] text-sm text-neutral-900">
              {capability.title}
            </span>
          )}
        </div>
        <CapabilityBadge status={capability.status} />
      </div>

      {denied && expanded && <MissingRules capability={capability} />}
    </li>
  );
}

function MissingRules({ capability }: { capability: Capability }) {
  return (
    <div className="border-t border-neutral-100 bg-neutral-50 px-4 py-3 pl-10">
      <p className="text-xs font-medium text-neutral-700">Missing permissions</p>
      {capability.missing_rules.length > 0 ? (
        <ul className="mt-2 space-y-1">
          {capability.missing_rules.map((rule, i) => (
            <li
              key={`${rule.resource}-${rule.subresource ?? ""}-${rule.verb}-${i}`}
              className="font-mono text-[11px] text-neutral-600"
            >
              {ruleLabel(rule)}
              {rule.reason && (
                <span className="text-neutral-400"> — {rule.reason}</span>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 text-xs text-neutral-500">
          No specific rules reported.
        </p>
      )}
      <p className="mt-2 text-xs text-neutral-500">
        Check this capability and use “Generate RBAC” below to grant it.
      </p>
    </div>
  );
}

// GenerateRbac is the footer action: check one or more capabilities above, then
// generate a single manifest granting the selection's full rule set. The YAML is
// built server-side (it is identity- and connection-method-aware), so this posts
// the selected keys and renders whatever the API returns — a ready-to-apply
// ClusterRole + ClusterRoleBinding, or best-effort guidance for connection
// methods with no addressable in-cluster subject.
function GenerateRbac({
  clusterId,
  selected,
}: {
  clusterId: string;
  selected: Set<string>;
}) {
  const [manifest, setManifest] = useState<string | null>(null);
  const [generating, setGenerating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A stable signature of the selection so the generated manifest is cleared
  // (it would be stale) whenever the checked set changes.
  const signature = useMemo(
    () => [...selected].sort().join(","),
    [selected],
  );
  useEffect(() => {
    setManifest(null);
    setError(null);
  }, [signature]);

  const count = selected.size;

  async function generate() {
    setGenerating(true);
    setError(null);
    const { data, error } = await api.POST(
      "/api/clusters/{id}/capabilities/rbac",
      {
        params: { path: { id: clusterId } },
        body: { keys: [...selected] },
      },
    );
    if (error) {
      setError(error.message ?? "Could not generate RBAC");
      setManifest(null);
    } else {
      setManifest(data?.manifest ?? "");
    }
    setGenerating(false);
  }

  return (
    <div className="border-t border-neutral-200 bg-neutral-50 px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        <p className="text-xs text-neutral-500">
          {count === 0
            ? "Select capabilities to grant, then generate a single manifest."
            : `${count} capabilit${count === 1 ? "y" : "ies"} selected.`}
        </p>
        <button
          type="button"
          onClick={() => void generate()}
          disabled={count === 0 || generating}
          className="shrink-0 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
        >
          {generating ? "Generating…" : "Generate RBAC"}
        </button>
      </div>
      {error && <p className="mt-2 text-xs text-red-600">{error}</p>}
      {manifest && <ManifestBlock yaml={manifest} />}
    </div>
  );
}

function ManifestBlock({ yaml }: { yaml: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(yaml);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard may be unavailable (e.g. insecure context); ignore.
    }
  }

  return (
    <div className="mt-3">
      <div className="flex items-center justify-between">
        <p className="text-xs text-neutral-500">Apply this to the cluster:</p>
        <button
          type="button"
          onClick={() => void copy()}
          className="inline-flex items-center gap-1 text-xs text-neutral-500 hover:text-neutral-900"
        >
          {copied ? (
            <>
              <Check className="h-3.5 w-3.5" />
              Copied
            </>
          ) : (
            <>
              <Copy className="h-3.5 w-3.5" />
              Copy
            </>
          )}
        </button>
      </div>
      <code className="mt-1 block max-h-72 overflow-auto whitespace-pre bg-neutral-900 p-3 font-mono text-[11px] text-neutral-100">
        {yaml}
      </code>
    </div>
  );
}

function CapabilityBadge({ status }: { status: Capability["status"] }) {
  if (status === "allowed") {
    return (
      <span className="inline-flex items-center gap-1 bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
        <CheckCircle2 className="h-3.5 w-3.5" />
        Allowed
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800">
      <XCircle className="h-3.5 w-3.5" />
      Denied
    </span>
  );
}

function ruleLabel(rule: CapabilityRule): string {
  const group = rule.api_group ? rule.api_group : "core";
  const resource = rule.subresource
    ? `${rule.resource}/${rule.subresource}`
    : rule.resource;
  return `${rule.verb} ${resource} (${group})`;
}

// groupByArea preserves first-seen area order so the layout is stable across
// re-checks (the API already returns current capabilities before placeholders).
function groupByArea(
  capabilities: Capability[],
): [string, Capability[]][] {
  const order: string[] = [];
  const byArea = new Map<string, Capability[]>();
  for (const cap of capabilities) {
    if (!byArea.has(cap.area)) {
      byArea.set(cap.area, []);
      order.push(cap.area);
    }
    byArea.get(cap.area)!.push(cap);
  }
  return order.map((area) => [area, byArea.get(area)!]);
}
