import { useEffect, useState } from "react";
import { connectLogStream } from "./logStream";
import type { StreamStatus } from "./resourceStream";

// Cap retained lines so a long-running follow can't grow memory unbounded; the
// viewer keeps the most recent MAX_LINES.
const MAX_LINES = 5_000;

// usePodLogs follows one pod's log stream and keeps the rolling tail of lines.
// `path` is the full /logs/stream URL (with container/tail/timestamps query);
// changing it (e.g. switching container) tears down and reopens the stream.
export function usePodLogs(
  path: string,
  enabled = true,
): { lines: string[]; status: StreamStatus; ended: boolean; error: string | null } {
  const [lines, setLines] = useState<string[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [ended, setEnded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!enabled) return;
    setLines([]);
    setEnded(false);
    setError(null);
    const ctrl = new AbortController();
    void connectLogStream(
      path,
      {
        onReset: () => setLines([]),
        onLine: (line) =>
          setLines((prev) => {
            const next = prev.length >= MAX_LINES ? prev.slice(1) : prev.slice();
            next.push(line);
            return next;
          }),
        onEnd: () => setEnded(true),
        onStatus: (s, err) => {
          setStatus(s);
          if (s === "live" || s === "connecting") setError(null);
          else if (err) setError(err);
        },
      },
      ctrl.signal,
    );
    return () => ctrl.abort();
  }, [path, enabled]);

  return { lines, status, ended, error };
}
