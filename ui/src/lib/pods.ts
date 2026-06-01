import type { components } from "../api/schema";

export type Cluster = components["schemas"]["Cluster"];
export type Pod = components["schemas"]["Pod"];

// PodRow is a pod paired with the cluster it was fetched from — the list view
// streams each cluster separately, so it tags every pod with its origin.
export type PodRow = Pod & { clusterId: string; clusterName: string };

// The sentinel value used by both filters to mean "no filter applied". Shared so
// the page and the namespace-syncing logic agree on it.
export const ALL = "all";

// podAge renders a pod's creation time as a compact relative age (the style
// kubectl uses: "5d", "3h", "12m"). Falls back to "—" for a missing/zero time.
export function podAge(createdAt: string): string {
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

// podStatusTone classifies a pod's display status into a colour tone for its
// badge: green for healthy/complete, red for failures, amber for in-progress
// or transitional states. It works off the kubectl-style status string the
// backend derives (Running, CrashLoopBackOff, Completed, Terminating, …).
export type StatusTone = "good" | "bad" | "warn" | "neutral";

export function podStatusTone(status: string, ready: boolean): StatusTone {
  if (BAD_STATUSES.has(status) || status.includes("BackOff") || status.startsWith("Err")) {
    return "bad";
  }
  if (status === "Completed" || status === "Succeeded") return "good";
  if (status === "Running") return ready ? "good" : "warn";
  if (TRANSIENT_STATUSES.has(status) || status.startsWith("Init:")) return "warn";
  return "neutral";
}

const BAD_STATUSES = new Set([
  "Failed",
  "Error",
  "CrashLoopBackOff",
  "ImagePullBackOff",
  "ErrImagePull",
  "CreateContainerError",
  "CreateContainerConfigError",
  "InvalidImageName",
  "OOMKilled",
  "Evicted",
  "NodeLost",
  "Unknown",
]);

const TRANSIENT_STATUSES = new Set([
  "Pending",
  "ContainerCreating",
  "PodInitializing",
  "Terminating",
  "NotReady",
  "ContainerStatusUnknown",
]);
