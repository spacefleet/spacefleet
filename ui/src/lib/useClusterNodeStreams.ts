import { useEffect, useMemo, useState } from "react";
import { connectResourceStream, type StreamStatus } from "./resourceStream";
import type { Cluster, Node, NodeRow } from "./nodes";

export interface ClusterNodeStreams {
  rows: NodeRow[];
  // Aggregate connection state across all watched clusters: "live" once any
  // stream is delivering.
  status: StreamStatus;
  // Per-cluster failures (e.g. a cluster that's unreachable), surfaced so an
  // empty table isn't mistaken for "no nodes".
  errors: { cluster: string; message: string }[];
}

// useClusterNodeStreams opens one live node stream per target cluster and merges
// the results into a single keyed list, tagging each node with its origin
// cluster. Streams open/close as the target set changes. It builds on the same
// transport as useResourceStream, but fans out over a dynamic set of clusters
// (which a single useResourceStream call can't, per the rules of hooks).
export function useClusterNodeStreams(targets: Cluster[]): ClusterNodeStreams {
  const [rows, setRows] = useState<Map<string, NodeRow>>(new Map());
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
      const rowKey = (n: Node) => `${cluster.id}/${n.name}`;
      const tag = (n: Node): NodeRow => ({
        ...n,
        clusterId: cluster.id,
        clusterName: cluster.name,
      });

      void connectResourceStream<Node>(
        `/api/clusters/${cluster.id}/nodes/stream`,
        {
          onSnapshot: (list) =>
            setRows((prev) => {
              const next = new Map(prev);
              for (const k of [...next.keys()])
                if (k.startsWith(prefix)) next.delete(k);
              for (const n of list) next.set(rowKey(n), tag(n));
              return next;
            }),
          onUpsert: (n) =>
            setRows((prev) => new Map(prev).set(rowKey(n), tag(n))),
          onDelete: (n) =>
            setRows((prev) => {
              const next = new Map(prev);
              next.delete(rowKey(n));
              return next;
            }),
          onStatus: (s, err) => {
            setStatusById((prev) => ({ ...prev, [cluster.id]: s }));
            setErrorById((prev) => {
              const next = { ...prev };
              // Surface the reason for both terminal errors and (ongoing)
              // reconnects, so a stuck stream is never silent. Clear it once the
              // cluster is healthy again.
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
