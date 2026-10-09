import {
  CheckCircle2,
  CircleDashed,
  Loader2,
  MinusCircle,
  PauseCircle,
  XCircle,
} from "lucide-react";
import type { components } from "../../api/schema";

type RunStatus = components["schemas"]["RunStatus"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// RunStatusBadge renders a workflow run's lifecycle, shared by the run view
// header and the history list. Hue is reserved for meaning (per the brand):
// neutral pending, blue running, green succeeded, amber partial, red failed,
// violet awaiting_approval (a distinct paused state — deliberately not amber,
// which means partial).
export function RunStatusBadge({ status }: { status: RunStatus }) {
  const styles: Record<RunStatus, string> = {
    pending: "bg-neutral-800 text-neutral-300",
    running: "bg-blue-500/15 text-blue-300",
    succeeded: "bg-green-500/15 text-green-300",
    partial: "bg-amber-500/15 text-amber-300",
    failed: "bg-red-500/15 text-red-300",
    awaiting_approval: "bg-violet-500/15 text-violet-300",
  };
  const Icon: Record<RunStatus, typeof CheckCircle2> = {
    pending: CircleDashed,
    running: Loader2,
    succeeded: CheckCircle2,
    partial: MinusCircle,
    failed: XCircle,
    awaiting_approval: PauseCircle,
  };
  const I = Icon[status];
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[status]}`}
    >
      <I className={`h-3.5 w-3.5 ${status === "running" ? "animate-spin" : ""}`} />
      {statusLabel(status)}
    </span>
  );
}

// statusLabel humanizes a status for display (e.g. awaiting_approval → "awaiting
// approval"); other statuses are already single words.
function statusLabel(status: RunStatus | ComponentRunStatus): string {
  return status.replace(/_/g, " ");
}

// ComponentStatusIcon renders the small status indicator drawn on a run node.
export function ComponentStatusIcon({ status }: { status: ComponentRunStatus }) {
  switch (status) {
    case "running":
      return <Loader2 className="h-3.5 w-3.5 animate-spin text-blue-400" />;
    case "succeeded":
      return <CheckCircle2 className="h-3.5 w-3.5 text-green-400" />;
    case "failed":
      return <XCircle className="h-3.5 w-3.5 text-red-400" />;
    case "skipped":
      return <MinusCircle className="h-3.5 w-3.5 text-neutral-500" />;
    case "awaiting_approval":
      return <PauseCircle className="h-3.5 w-3.5 text-violet-400" />;
    case "pending":
    default:
      return <CircleDashed className="h-3.5 w-3.5 text-neutral-500" />;
  }
}
