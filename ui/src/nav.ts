import {
  AppWindow,
  LayoutDashboard,
  Server,
  Shield,
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
  // A section is one of two shapes: a direct link (set `path`, omit `items` —
  // the sidebar renders it as a single clickable row, e.g. Dashboard) or an
  // expandable group (set `items` — the sidebar renders a toggle that reveals
  // the sub-pages).
  path?: string;
  items?: NavLeaf[];
  // When true, the section is only shown to organization admins (it holds
  // org-management pages). The pages themselves also guard server-side.
  adminOnly?: boolean;
  // When true, the sidebar pins this section to the bottom of the rail rather
  // than the top-aligned flow (the Admin section sits at the bottom).
  footer?: boolean;
}

export const navSections: NavSection[] = [
  {
    label: "Dashboard",
    icon: LayoutDashboard,
    path: "/",
  },
  {
    label: "Applications",
    icon: AppWindow,
    items: [{ label: "All Apps", path: "/applications" }],
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
    label: "Admin",
    icon: Shield,
    adminOnly: true,
    footer: true,
    items: [
      { label: "Members", path: "/admin/members" },
      { label: "Clusters", path: "/admin/clusters" },
      { label: "Private Charts", path: "/admin/private-charts" },
    ],
  },
];

// Flattened list of every leaf, paired with its section. Used to generate routes
// and to resolve the current page's title/breadcrumb. Direct-link sections
// (those without `items`) contribute no leaves — their route is wired directly.
export const navLeaves: { section: NavSection; leaf: NavLeaf }[] =
  navSections.flatMap((section) =>
    (section.items ?? []).map((leaf) => ({ section, leaf })),
  );
