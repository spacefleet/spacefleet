import type { components } from "../api/schema";

export type Cluster = components["schemas"]["Cluster"];
export type Node = components["schemas"]["Node"];

// NodeRow is a node paired with the cluster it was fetched from — the list view
// queries each cluster separately, so it tags every node with its origin.
export type NodeRow = Node & { clusterId: string; clusterName: string };

// nodeAge renders a node's creation time as a compact relative age (the style
// kubectl uses: "5d", "3h", "12m"). Falls back to "—" for a missing/zero time.
export function nodeAge(createdAt: string): string {
  const created = new Date(createdAt).getTime();
  if (!created || Number.isNaN(created)) return "—";
  const secs = Math.max(0, Math.floor((Date.now() - created) / 1000));
  const days = Math.floor(secs / 86400);
  if (days > 0) return `${days}d`;
  const hours = Math.floor(secs / 3600);
  if (hours > 0) return `${hours}h`;
  const mins = Math.floor(secs / 60);
  if (mins > 0) return `${mins}m`;
  return `${secs}s`;
}

// nodeRolesLabel renders the roles list the way kubectl does, defaulting to
// "<none>" when a node carries no role labels.
export function nodeRolesLabel(roles: string[]): string {
  return roles.length > 0 ? roles.join(", ") : "<none>";
}
