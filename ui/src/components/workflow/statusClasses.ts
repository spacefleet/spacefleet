import type { components } from "../../api/schema";

type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// componentStatusClasses returns the border/background classes for a run node,
// colored by its component-run status. Neutral pending, blue running, green
// succeeded, red failed, muted skipped. Hue is reserved for meaning per brand.
export function componentStatusClasses(status: ComponentRunStatus): string {
  switch (status) {
    case "running":
      return "border-blue-400 bg-blue-50";
    case "succeeded":
      return "border-green-500 bg-green-50";
    case "failed":
      return "border-red-500 bg-red-50";
    case "skipped":
      return "border-neutral-200 bg-neutral-50 opacity-60";
    case "awaiting_approval":
      return "border-violet-400 bg-violet-50";
    case "pending":
    default:
      return "border-neutral-300 bg-white";
  }
}

// componentStatusFill returns the solid fill for a status segment in a stage
// bar (one block per stage). Same hue meanings as componentStatusClasses, at
// full strength so a thin segment still reads.
export function componentStatusFill(status: ComponentRunStatus): string {
  switch (status) {
    case "running":
      return "bg-blue-500 animate-pulse";
    case "succeeded":
      return "bg-green-500";
    case "failed":
      return "bg-red-500";
    case "skipped":
      return "bg-neutral-300";
    case "awaiting_approval":
      return "bg-violet-500";
    case "pending":
    default:
      return "bg-neutral-200";
  }
}
