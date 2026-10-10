import type { components } from "../../api/schema";

type ComponentType = components["schemas"]["ComponentType"];

// The slice of a component the stage views read — shared by the builder's
// editable components and the API's saved ones.
export interface StageViewComponent {
  id: string;
  name: string;
  type: ComponentType;
  config: Record<string, string>;
  continue_on_failure: boolean;
  requires_approval?: boolean;
  target_cluster_id?: string | null;
  target_namespace?: string;
}

export interface StageViewStage {
  id: string;
  name: string;
  components: StageViewComponent[];
}

// The product name of each component type, for headings ("OpenTofu", not the
// API's "terraform").
export const COMPONENT_TYPE_LABELS: Record<ComponentType, string> = {
  helm: "Helm",
  manifest: "Manifest",
  terraform: "OpenTofu",
};

// componentSummary is the one-line "where does this go" under a component's
// name: the deploy target for helm (cluster / namespace) and manifest
// (cluster), the module path for OpenTofu. Empty when nothing is set yet.
export function componentSummary(
  c: StageViewComponent,
  clusterName: (id: string) => string | undefined,
): string {
  const cluster = c.target_cluster_id
    ? (clusterName(c.target_cluster_id) ?? "")
    : "";
  switch (c.type) {
    case "helm":
      return [cluster, c.target_namespace].filter(Boolean).join(" / ");
    case "manifest":
      return [cluster, c.config.path].filter(Boolean).join(" · ");
    case "terraform":
      return c.config.path || c.config.repo_url || "";
    default:
      return "";
  }
}
