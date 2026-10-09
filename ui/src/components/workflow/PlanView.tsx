import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import type { components } from "../../api/schema";
import { DiffView } from "../DiffView";

type PlanSummary = components["schemas"]["PlanSummary"];
type PlanResourceChange = components["schemas"]["PlanResourceChange"];
type PlanAction = PlanResourceChange["action"];

// PlanCounts is the compact "+2 ~1 -1" readout of an OpenTofu plan — the
// add/change/destroy totals, each coloured like the plan text's own symbols,
// with a replace count when any resource is recreated. It is small enough to
// sit on a run node or a tab label; PlanSummaryBar is the full-width version.
export function PlanCounts({ plan }: { plan: PlanSummary }) {
  if (!plan.has_changes) {
    return (
      <span className="inline-flex items-center px-1.5 py-0.5 text-[10px] font-medium bg-neutral-800 text-neutral-300">
        no changes
      </span>
    );
  }
  return (
    <span
      className="inline-flex items-center gap-1.5 font-mono text-[11px] font-medium"
      title={planTitle(plan)}
    >
      <span className="text-emerald-300">+{plan.add}</span>
      <span className="text-amber-300">~{plan.change}</span>
      <span className="text-red-300">-{plan.destroy}</span>
      {plan.replace > 0 && (
        <span className="text-red-300" title="destroy and recreate">
          ±{plan.replace}
        </span>
      )}
    </span>
  );
}

function planTitle(plan: PlanSummary): string {
  const parts = [
    `${plan.add} to add`,
    `${plan.change} to change`,
    `${plan.destroy} to destroy`,
  ];
  if (plan.replace > 0) parts.push(`${plan.replace} replaced`);
  if (plan.outputs_changed) parts.push("outputs change");
  return parts.join(", ");
}

// PlanSummaryBar is the headline an approver reads before deciding: the totals
// in words, a loud callout when anything is destroyed or replaced, and the
// outputs note. Rendered above the resource list on a plan step.
export function PlanSummaryBar({ plan }: { plan: PlanSummary }) {
  const destructive = plan.destroy > 0 || plan.replace > 0;
  const driftCount = plan.drift?.length ?? 0;

  // A drift check proposes nothing: its whole verdict is whether anything
  // changed outside of OpenTofu.
  if (plan.refresh_only) {
    return (
      <div
        className={`flex flex-wrap items-center gap-x-4 gap-y-1 border px-3 py-2 text-sm ${
          plan.has_drift
            ? "border-amber-500/30 bg-amber-500/10 text-amber-200"
            : "border-neutral-800 bg-neutral-800/50 text-neutral-300"
        }`}
      >
        {plan.has_drift ? (
          <span className="font-medium">
            Drift detected: {driftCount} resource{driftCount === 1 ? "" : "s"}{" "}
            changed outside of OpenTofu since the last apply.
          </span>
        ) : (
          <span className="font-medium">
            No drift. Real infrastructure still matches the last apply.
          </span>
        )}
      </div>
    );
  }

  return (
    <div
      className={`flex flex-wrap items-center gap-x-4 gap-y-1 border px-3 py-2 text-sm ${
        !plan.has_changes
          ? "border-neutral-800 bg-neutral-800/50 text-neutral-300"
          : destructive
            ? "border-red-500/30 bg-red-500/10 text-red-200"
            : "border-emerald-500/30 bg-emerald-500/10 text-emerald-200"
      }`}
    >
      {plan.has_drift && (
        <span className="w-full text-xs">
          {driftCount} resource{driftCount === 1 ? "" : "s"} changed outside of
          OpenTofu since the last apply — see the drift section below.
        </span>
      )}
      {plan.has_changes ? (
        <>
          <span className="font-medium">
            Plan: {plan.add} to add, {plan.change} to change, {plan.destroy} to
            destroy.
          </span>
          {plan.replace > 0 && (
            <span>
              {plan.replace} resource{plan.replace === 1 ? "" : "s"} will be
              destroyed and recreated.
            </span>
          )}
          {plan.outputs_changed && <span>Output values change.</span>}
        </>
      ) : (
        <span className="font-medium">
          No changes. Infrastructure matches the configuration.
        </span>
      )}
    </div>
  );
}

// PlanResourceList lists each planned resource action with its address, most
// destructive first, each expandable to the resource's own block of the plan
// text (its attribute-level diff) when the API supplied it — it is withheld
// below editor, in which case the row is a plain, non-expandable line.
export function PlanResourceList({ plan }: { plan: PlanSummary }) {
  const rows = [...plan.resources].sort(
    (a, b) => actionRank(a.action) - actionRank(b.action),
  );
  const drift = plan.drift ?? [];
  return (
    <div className="flex flex-col gap-3">
      {drift.length > 0 && (
        <div>
          <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-amber-300">
            Changed outside of OpenTofu
          </p>
          <ul className="divide-y divide-amber-500/20 border border-amber-500/30">
            {drift.map((r) => (
              <PlanResourceRow key={"drift:" + r.address} change={r} />
            ))}
          </ul>
        </div>
      )}
      {plan.refresh_only ? null : rows.length === 0 ? (
        <p className="text-sm text-neutral-400">
          {plan.has_changes
            ? "No resource changes (outputs only)."
            : "No resources change."}
        </p>
      ) : (
        <div>
          {drift.length > 0 && (
            <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
              Planned actions
            </p>
          )}
          <ul className="divide-y divide-neutral-800 border border-neutral-800">
            {rows.map((r) => (
              <PlanResourceRow key={r.address + r.action} change={r} />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function PlanResourceRow({ change }: { change: PlanResourceChange }) {
  const [open, setOpen] = useState(false);
  const expandable = !!change.diff;
  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <li>
      <button
        type="button"
        onClick={() => expandable && setOpen((o) => !o)}
        disabled={!expandable}
        aria-expanded={expandable ? open : undefined}
        className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm ${
          expandable ? "hover:bg-neutral-800" : "cursor-default"
        }`}
      >
        {expandable ? (
          <Chevron className="h-3.5 w-3.5 shrink-0 text-neutral-500" />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <ActionBadge action={change.action} />
        <span className="truncate font-mono text-xs text-neutral-100">
          {change.address}
        </span>
        {change.detail && change.action !== "create" && (
          <span className="ml-auto hidden truncate text-xs text-neutral-400 sm:inline">
            {change.detail}
          </span>
        )}
      </button>
      {open && change.diff && (
        <div className="px-3 pb-3">
          <DiffView diff={change.diff} className="max-h-[32rem]" />
        </div>
      )}
    </li>
  );
}

// ActionBadge is the fixed-width action label: the plan symbol plus the word,
// coloured like the plan text's own legend.
export function ActionBadge({ action }: { action: PlanAction }) {
  const { symbol, label, className } = actionStyle(action);
  return (
    <span
      className={`inline-flex w-[5.5rem] shrink-0 items-center gap-1 px-1.5 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wide ${className}`}
    >
      <span>{symbol}</span>
      {label}
    </span>
  );
}

function actionStyle(action: PlanAction): {
  symbol: string;
  label: string;
  className: string;
} {
  switch (action) {
    case "create":
      return { symbol: "+", label: "create", className: "bg-emerald-500/15 text-emerald-300" };
    case "update":
      return { symbol: "~", label: "update", className: "bg-amber-500/15 text-amber-300" };
    case "replace":
      return { symbol: "-/+", label: "replace", className: "bg-red-500/15 text-red-300" };
    case "delete":
      return { symbol: "-", label: "destroy", className: "bg-red-500/15 text-red-300" };
    case "read":
      return { symbol: "<=", label: "read", className: "bg-sky-500/15 text-sky-300" };
    case "move":
      return { symbol: "→", label: "move", className: "bg-neutral-800 text-neutral-300" };
    case "import":
      return { symbol: "←", label: "import", className: "bg-sky-500/15 text-sky-300" };
    case "forget":
      return { symbol: "·", label: "forget", className: "bg-neutral-800 text-neutral-300" };
    case "drift_update":
      return { symbol: "~", label: "changed", className: "bg-amber-500/15 text-amber-300" };
    case "drift_delete":
      return { symbol: "-", label: "deleted", className: "bg-red-500/15 text-red-300" };
    default:
      return { symbol: "?", label: "other", className: "bg-neutral-800 text-neutral-300" };
  }
}

// actionRank orders the resource list so the actions an approver most needs
// to notice come first.
function actionRank(action: PlanAction): number {
  switch (action) {
    case "delete":
      return 0;
    case "replace":
      return 1;
    case "update":
      return 2;
    case "create":
      return 3;
    case "import":
      return 4;
    case "move":
      return 5;
    case "forget":
      return 6;
    case "read":
      return 7;
    default:
      return 8;
  }
}
