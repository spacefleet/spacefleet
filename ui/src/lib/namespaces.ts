import type { components } from "../api/schema";

export type Namespace = components["schemas"]["Namespace"];

// NamespaceRow is a namespace paired with the cluster it was fetched from — the
// list view queries each cluster separately, so it tags every namespace with
// its origin (namespace names are only unique within a single cluster).
export type NamespaceRow = Namespace & {
  clusterId: string;
  clusterName: string;
};

// namespacePhase normalizes the reported status to one of the phases we render,
// defaulting to "Active" when the cluster reports nothing (a freshly-created
// namespace can briefly have an empty phase).
export function namespacePhase(status: string): "Active" | "Terminating" {
  return status === "Terminating" ? "Terminating" : "Active";
}
