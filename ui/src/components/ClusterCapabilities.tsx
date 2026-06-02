import { useCallback, useEffect, useState } from "react";
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
// allowed/denied. Denied capabilities get an expandable "How to enable" block
// with the missing RBAC rules and copy-paste remediation YAML.
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
        <CapabilityReport report={report} />
      )}
    </div>
  );
}

function CapabilityReport({ report }: { report: ClusterCapabilities }) {
  const groups = groupByArea(report.capabilities);

  return (
    <div>
      <IdentityLine identity={report.identity} />
      {groups.length === 0 ? (
        <p className="p-4 text-sm text-neutral-500">
          No capabilities reported.
        </p>
      ) : (
        groups.map(([area, caps]) => (
          <AreaGroup
            key={area}
            area={area}
            capabilities={caps}
            remediation={report.remediation}
          />
        ))
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
  remediation,
}: {
  area: string;
  capabilities: Capability[];
  remediation?: string;
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
            remediation={remediation}
          />
        ))}
      </ul>
    </div>
  );
}

function CapabilityRow({
  capability,
  remediation,
}: {
  capability: Capability;
  remediation?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const denied = capability.status === "denied";
  // Prefer the manifest scoped to just this capability so the operator can grant
  // it alone; fall back to the report-level guidance (methods without an
  // addressable in-cluster subject only carry the report-level form).
  const scopedRemediation = capability.remediation ?? remediation;

  return (
    <li>
      <div className="flex items-center justify-between px-4 py-2">
        <div className="flex items-center gap-2">
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

      {denied && expanded && (
        <HowToEnable capability={capability} remediation={scopedRemediation} />
      )}
    </li>
  );
}

function HowToEnable({
  capability,
  remediation,
}: {
  capability: Capability;
  remediation?: string;
}) {
  return (
    <div className="border-t border-neutral-100 bg-neutral-50 px-4 py-3">
      <p className="text-xs font-medium text-neutral-700">How to enable</p>
      {capability.missing_rules.length > 0 && (
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
      )}
      {remediation ? (
        <RemediationBlock yaml={remediation} />
      ) : (
        <p className="mt-2 text-xs text-neutral-500">
          Grant the permissions above to the resolved identity, then re-check.
        </p>
      )}
    </div>
  );
}

function RemediationBlock({ yaml }: { yaml: string }) {
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
