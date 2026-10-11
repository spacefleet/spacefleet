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
// absolute.
//
// The platform is split into areas (Overview, Applications, Clusters). The
// sidebar shows one area at a time: a switcher at the top picks the area, and
// the rail below lists only that area's pages. The area follows the route, so
// following a link into another area switches the sidebar with it.
export interface NavLeaf {
  label: string;
  path: string;
  // Optional sub-heading this leaf belongs to within its area. Purely
  // informational: the sidebar renders a non-interactive label above the first
  // leaf of each group (e.g. grouping the Clusters resource pages into
  // "Cluster-scoped" vs. "Namespaced"). Leaves without a group render directly
  // under the switcher.
  group?: string;
}

export interface NavArea {
  id: "overview" | "applications" | "clusters";
  label: string;
  icon: LucideIcon;
  // One line under the label in the area switcher.
  description: string;
  // Where switching to this area lands.
  path: string;
  // The route prefixes this area owns: a route under one of them shows this
  // area in the sidebar. Overview has none — it owns only "/" itself.
  prefixes: string[];
  items: NavLeaf[];
}

// The Admin section: org-management pages, shown only to organization admins
// (the pages themselves also guard server-side) and pinned to the bottom of
// the rail under every area, as an expandable group.
export interface NavSection {
  label: string;
  icon: LucideIcon;
  items: NavLeaf[];
}

const applications: NavArea = {
  id: "applications",
  label: "Applications",
  icon: AppWindow,
  description: "Apps and their workflow runs",
  path: "/applications",
  prefixes: ["/applications", "/runs"],
  items: [
    { label: "All Apps", path: "/applications" },
    { label: "Workflow Runs", path: "/runs" },
  ],
};

const clusters: NavArea = {
  id: "clusters",
  label: "Clusters",
  icon: Server,
  description: "Clusters and what runs on them",
  path: "/clusters/namespaces",
  prefixes: ["/clusters"],
  items: [
    // Cluster-level resources (not scoped to a namespace).
    { label: "Nodes", path: "/clusters/nodes", group: "Cluster-scoped" },
    {
      label: "Namespaces",
      path: "/clusters/namespaces",
      group: "Cluster-scoped",
    },
    // Namespace-level resources.
    { label: "Pods", path: "/clusters/pods", group: "Namespaced" },
  ],
};

// Overview is the landing area (the "/" dashboard, served by Home). Its rail
// lists the other areas, so its leaves are links into them rather than pages
// of its own.
const overview: NavArea = {
  id: "overview",
  label: "Overview",
  icon: LayoutDashboard,
  description: "Your organization at a glance",
  path: "/",
  prefixes: [],
  items: [applications, clusters].map((a) => ({ label: a.label, path: a.path })),
};

export const navAreas: NavArea[] = [overview, applications, clusters];

export const adminSection: NavSection = {
  label: "Admin",
  icon: Shield,
  items: [
    { label: "Members", path: "/admin/members" },
    { label: "Clusters", path: "/admin/clusters" },
    { label: "Cloud Credentials", path: "/admin/cloud-credentials" },
    { label: "Private Charts", path: "/admin/private-charts" },
    { label: "GitHub", path: "/admin/github" },
    { label: "Notifications", path: "/admin/notifications" },
    { label: "Policies", path: "/admin/policies" },
  ],
};

// underPath reports whether pathname is path itself or a route beneath it.
export function underPath(pathname: string, path: string): boolean {
  return pathname === path || pathname.startsWith(`${path}/`);
}

// areaForPath resolves the area a route belongs to, or undefined for routes
// outside every area (Admin pages, not-found).
export function areaForPath(pathname: string): NavArea | undefined {
  if (pathname === "/") return overview;
  return navAreas.find((a) => a.prefixes.some((p) => underPath(pathname, p)));
}

// Flattened list of every page leaf, paired with the area or section that owns
// it. Used to generate routes and to resolve the current page's title and
// breadcrumb. Overview's leaves point into the other areas, so it contributes
// none — "/" is wired directly.
export const navLeaves: { section: NavArea | NavSection; leaf: NavLeaf }[] = [
  applications,
  clusters,
  adminSection,
].flatMap((section) => section.items.map((leaf) => ({ section, leaf })));
