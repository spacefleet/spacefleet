import { useEffect, useRef, useState } from "react";
import {
  connectResourceStream,
  type StreamStatus,
} from "./resourceStream";

// useResourceStream subscribes to a single SSE resource stream and maintains the
// live set of items keyed by `key`. It's the building block for any one-stream
// view (a cluster's nodes, a namespace's pods, …); the Nodes list fans out over
// several clusters with useClusterNodeStreams, which shares the same transport.
export function useResourceStream<T>(
  path: string,
  key: (item: T) => string,
  enabled = true,
): { items: T[]; status: StreamStatus; error: string | null } {
  const [items, setItems] = useState<Map<string, T>>(new Map());
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [error, setError] = useState<string | null>(null);

  // Hold key in a ref so changing its identity doesn't tear down the stream.
  const keyRef = useRef(key);
  keyRef.current = key;

  useEffect(() => {
    if (!enabled) return;
    setItems(new Map());
    setError(null);
    const ctrl = new AbortController();
    void connectResourceStream<T>(
      path,
      {
        onSnapshot: (list) =>
          setItems(() => {
            const next = new Map<string, T>();
            for (const item of list) next.set(keyRef.current(item), item);
            return next;
          }),
        onUpsert: (item) =>
          setItems((prev) => new Map(prev).set(keyRef.current(item), item)),
        onDelete: (item) =>
          setItems((prev) => {
            const next = new Map(prev);
            next.delete(keyRef.current(item));
            return next;
          }),
        onStatus: (s, err) => {
          setStatus(s);
          // Keep the reason visible across reconnects, not just terminal
          // errors; clear it once the stream is healthy.
          if (s === "live" || s === "connecting") setError(null);
          else if (err) setError(err);
        },
      },
      ctrl.signal,
    );
    return () => ctrl.abort();
  }, [path, enabled]);

  return { items: [...items.values()], status, error };
}
