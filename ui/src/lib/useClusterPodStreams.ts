import { useEffect, useMemo, useState } from "react";
import { connectResourceStream, type StreamStatus } from "./resourceStream";
import type { Cluster, Pod, PodRow } from "./pods";

export interface ClusterPodStreams {
  rows: PodRow[];
  // Aggregate connection state across all watched clusters: "live" once any
  // stream is delivering.
  status: StreamStatus;
  // Per-cluster failures (e.g. a cluster that's unreachable), surfaced so an
  // empty table isn't mistaken for "no pods".
  errors: { cluster: string; message: string }[];
}

// useClusterPodStreams opens one live pod stream per target cluster and merges
// the results into a single keyed list, tagging each pod with its origin
// cluster. It is the pod counterpart of useClusterNodeStreams — each stream
// carries every namespace, so namespace filtering is a cheap client-side step
// over the merged rows (no stream re-open when the namespace filter changes).
export function useClusterPodStreams(targets: Cluster[]): ClusterPodStreams {
  const [rows, setRows] = useState<Map<string, PodRow>>(new Map());
  const [statusById, setStatusById] = useState<Record<string, StreamStatus>>({});
  const [errorById, setErrorById] = useState<Record<string, string>>({});

  // Re-open streams only when the *set* of clusters changes, not on every
  // render. The id+name key also reopens on a rename so rows relabel.
  const targetKey = targets
    .map((c) => `${c.id}:${c.name}`)
    .sort()
    .join(",");

  useEffect(() => {
    setRows(new Map());
    setStatusById({});
    setErrorById({});
    const controllers: AbortController[] = [];

    for (const cluster of targets) {
      const ctrl = new AbortController();
      controllers.push(ctrl);
      const prefix = `${cluster.id}/`;
      // Pods are unique per (namespace, name) within a cluster, so the key
      // includes the namespace.
      const rowKey = (p: Pod) => `${cluster.id}/${p.namespace}/${p.name}`;
      const tag = (p: Pod): PodRow => ({
        ...p,
        clusterId: cluster.id,
        clusterName: cluster.name,
      });

      void connectResourceStream<Pod>(
        `/api/clusters/${cluster.id}/pods/stream`,
        {
          onSnapshot: (list) =>
            setRows((prev) => {
              const next = new Map(prev);
              for (const k of [...next.keys()])
                if (k.startsWith(prefix)) next.delete(k);
              for (const p of list) next.set(rowKey(p), tag(p));
              return next;
            }),
          onUpsert: (p) =>
            setRows((prev) => new Map(prev).set(rowKey(p), tag(p))),
          onDelete: (p) =>
            setRows((prev) => {
              const next = new Map(prev);
              next.delete(rowKey(p));
              return next;
            }),
          onStatus: (s, err) => {
            setStatusById((prev) => ({ ...prev, [cluster.id]: s }));
            setErrorById((prev) => {
              const next = { ...prev };
              if ((s === "error" || s === "reconnecting") && err)
                next[cluster.id] = err;
              else if (s === "live" || s === "connecting")
                delete next[cluster.id];
              return next;
            });
          },
        },
        ctrl.signal,
      );
    }

    return () => controllers.forEach((c) => c.abort());
    // targetKey encodes the cluster set; the cluster objects are read fresh
    // inside but are stable for a given key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetKey]);

  const status = useMemo<StreamStatus>(() => {
    const all = Object.values(statusById);
    if (all.length === 0) return "connecting";
    if (all.includes("live")) return "live";
    if (all.includes("connecting")) return "connecting";
    if (all.includes("reconnecting")) return "reconnecting";
    return "error";
  }, [statusById]);

  const errors = Object.entries(errorById).map(([id, message]) => ({
    cluster: targets.find((c) => c.id === id)?.name ?? id,
    message,
  }));

  return { rows: [...rows.values()], status, errors };
}
