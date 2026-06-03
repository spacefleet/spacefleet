import { useEffect, useState } from "react";
import { connectObjectStream } from "./objectStream";
import { type StreamStatus } from "./resourceStream";

// useObjectStream subscribes to a single-object SSE stream and keeps the latest
// value. Used for the Tekton install status stream and a TaskRun status stream —
// realtime, no polling. Changing `path` (or toggling `enabled`) tears down and
// reopens the stream.
export function useObjectStream<T>(
  path: string,
  enabled = true,
): { value: T | null; status: StreamStatus; error: string | null } {
  const [value, setValue] = useState<T | null>(null);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!enabled) return;
    setValue(null);
    setError(null);
    const ctrl = new AbortController();
    void connectObjectStream<T>(
      path,
      {
        onUpdate: (v) => setValue(v),
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

  return { value, status, error };
}
