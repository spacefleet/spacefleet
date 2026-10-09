import { Package, FileCode, Layers } from "lucide-react";
import type { components } from "../../api/schema";

type ComponentType = components["schemas"]["ComponentType"];

// TypeBadge labels a component with its type (helm/manifest/terraform) — sharp
// corners, neutral palette, a small leading glyph.
export function TypeBadge({ type }: { type: ComponentType }) {
  const Icon =
    type === "helm" ? Package : type === "terraform" ? Layers : FileCode;
  return (
    <span className="inline-flex items-center gap-1 border border-neutral-700 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-neutral-400">
      <Icon className="h-3 w-3" />
      {type}
    </span>
  );
}
