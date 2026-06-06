import { CheckCircle2, CircleDashed, Loader2, MinusCircle, XCircle } from "lucide-react";
import type { components } from "../../api/schema";

type RunStatus = components["schemas"]["RunStatus"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// RunStatusBadge renders a workflow run's lifecycle, shared by the run view
// header and the history list. Hue is reserved for meaning (per the brand):
// neutral pending, blue running, green succeeded, amber partial, red failed.
export function RunStatusBadge({ status }: { status: RunStatus }) {
  const styles: Record<RunStatus, string> = {
    pending: "bg-neutral-100 text-neutral-700",
    running: "bg-blue-100 text-blue-800",
    succeeded: "bg-green-100 text-green-800",
    partial: "bg-amber-100 text-amber-800",
    failed: "bg-red-100 text-red-800",
  };
  const Icon: Record<RunStatus, typeof CheckCircle2> = {
    pending: CircleDashed,
    running: Loader2,
    succeeded: CheckCircle2,
    partial: MinusCircle,
    failed: XCircle,
  };
  const I = Icon[status];
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium ${styles[status]}`}
    >
      <I className={`h-3.5 w-3.5 ${status === "running" ? "animate-spin" : ""}`} />
      {status}
    </span>
  );
}

// ComponentStatusIcon renders the small status indicator drawn on a run node.
export function ComponentStatusIcon({ status }: { status: ComponentRunStatus }) {
  switch (status) {
    case "running":
      return <Loader2 className="h-3.5 w-3.5 animate-spin text-blue-600" />;
    case "succeeded":
      return <CheckCircle2 className="h-3.5 w-3.5 text-green-600" />;
    case "failed":
      return <XCircle className="h-3.5 w-3.5 text-red-600" />;
    case "skipped":
      return <MinusCircle className="h-3.5 w-3.5 text-neutral-400" />;
    case "pending":
    default:
      return <CircleDashed className="h-3.5 w-3.5 text-neutral-400" />;
  }
}
