import {
  AppWindow,
  CircleDollarSign,
  LayoutDashboard,
  Plug,
  Server,
  ShieldCheck,
  type LucideIcon,
} from "lucide-react";

// Single source of truth for the app's primary navigation. The sidebar renders
// it, and App.tsx generates a route per leaf from it — so adding a page is a
// one-line edit here plus (optionally) a real component in App.tsx. Paths are
// absolute; the leaf whose path is "/" is the app's index (the Dashboard
// overview, served by Home).
export interface NavLeaf {
  label: string;
  path: string;
  // Optional sub-heading this leaf belongs to within its section. Purely
  // informational: the sidebar renders a non-interactive label above the first
  // leaf of each group (e.g. grouping Infrastructure into "Cluster" vs.
  // "Namespaced" resources). Leaves without a group render directly under the
  // section, as before.
  group?: string;
}

export interface NavSection {
  label: string;
  icon: LucideIcon;
  items: NavLeaf[];
}

export const navSections: NavSection[] = [
  {
    label: "Dashboard",
    icon: LayoutDashboard,
    items: [
      { label: "Overview", path: "/" },
      { label: "Activity", path: "/activity" },
      { label: "Alerts", path: "/alerts" },
    ],
  },
  {
    label: "Applications",
    icon: AppWindow,
    items: [
      { label: "All Apps", path: "/applications" },
      { label: "Deployments", path: "/applications/deployments" },
      { label: "Environments", path: "/applications/environments" },
      { label: "Domains", path: "/applications/domains" },
      { label: "Logs", path: "/applications/logs" },
    ],
  },
  {
    label: "Infrastructure",
    icon: Server,
    items: [
      // Cluster-level resources (not scoped to a namespace).
      { label: "Nodes", path: "/infrastructure/nodes", group: "Cluster" },
      {
        label: "Namespaces",
        path: "/infrastructure/namespaces",
        group: "Cluster",
      },
      // Namespace-level resources.
      { label: "Pods", path: "/infrastructure/pods", group: "Namespaced" },
    ],
  },
  {
    label: "Security",
    icon: ShieldCheck,
    items: [
      { label: "Findings", path: "/security/findings" },
      { label: "Policies", path: "/security/policies" },
      { label: "Access", path: "/security/access" },
      { label: "Secrets", path: "/security/secrets" },
      { label: "Audit Log", path: "/security/audit" },
    ],
  },
  {
    label: "Cost",
    icon: CircleDollarSign,
    items: [
      { label: "Overview", path: "/cost" },
      { label: "Breakdown", path: "/cost/breakdown" },
      { label: "Budgets", path: "/cost/budgets" },
      { label: "Recommendations", path: "/cost/recommendations" },
    ],
  },
  {
    label: "Providers",
    icon: Plug,
    items: [
      { label: "Git", path: "/providers/git" },
      { label: "Clusters", path: "/providers/clusters" },
      { label: "CI/CD", path: "/providers/cicd" },
      { label: "Monitoring", path: "/providers/monitoring" },
    ],
  },
];

// Flattened list of every leaf, paired with its section. Used to generate routes
// and to resolve the current page's title/breadcrumb.
export const navLeaves: { section: NavSection; leaf: NavLeaf }[] =
  navSections.flatMap((section) =>
    section.items.map((leaf) => ({ section, leaf })),
  );
