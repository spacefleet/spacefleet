import { fetchEventSource } from "@microsoft/fetch-event-source";
import { authToken, currentOrgId } from "../api/client";
import type { StreamStatus } from "./resourceStream";

// Client for the backend's pod-log SSE stream. It is the log-shaped sibling of
// connectResourceStream (same auth headers, same own-the-reconnect loop), but
// the payload is a flat sequence of `log` lines rather than resource deltas, so
// it has its own tiny handler shape. The stream ends with an `eof` event when
// the container's log source closes; we then stop instead of reconnecting.

export interface LogStreamHandlers {
  // A fresh connection opened — clear any prior lines, since a reconnect
  // re-tails the backlog and would otherwise duplicate it.
  onReset: () => void;
  onLine: (line: string) => void;
  // The server signalled the log source closed (container exited / follow
  // ended). No more lines will arrive on this stream.
  onEnd: () => void;
  onStatus: (status: StreamStatus, error?: string) => void;
}

class FatalStreamError extends Error {}

const MAX_BACKOFF_MS = 15_000;

function backoffMs(attempt: number): number {
  return Math.min(MAX_BACKOFF_MS, 1_000 * 2 ** Math.min(attempt, 4));
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

// connectLogStream holds a live connection to a pod-log endpoint, reconnecting
// with backoff until `signal` aborts or the server sends `eof`.
export async function connectLogStream(
  path: string,
  handlers: LogStreamHandlers,
  signal: AbortSignal,
): Promise<void> {
  let attempt = 0;
  let ended = false;
  while (!signal.aborted && !ended) {
    handlers.onStatus(attempt === 0 ? "connecting" : "reconnecting");

    const token = await authToken();
    const orgId = currentOrgId();
    const headers: Record<string, string> = { Accept: "text/event-stream" };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (orgId) headers["X-Organization-ID"] = orgId;

    try {
      await fetchEventSource(path, {
        headers,
        signal,
        openWhenHidden: true,
        onopen: async (res) => {
          const contentType = res.headers.get("content-type") ?? "";
          if (res.ok && contentType.includes("text/event-stream")) {
            attempt = 0;
            handlers.onReset();
            handlers.onStatus("live");
            return;
          }
          const body = (await res.json().catch(() => null)) as {
            message?: string;
          } | null;
          const message = body?.message ?? `stream failed (${res.status})`;
          if ([400, 403, 404].includes(res.status)) {
            throw new FatalStreamError(message);
          }
          throw new Error(message);
        },
        onmessage(msg) {
          if (msg.event === "eof") {
            ended = true;
            handlers.onEnd();
            return;
          }
          if (msg.event !== "log" || !msg.data) return;
          const data = JSON.parse(msg.data) as { line: string };
          handlers.onLine(data.line);
        },
        onclose() {
          // A clean server close mid-follow (e.g. the lifetime cap) should
          // reconnect; surface it as a retriable error to our loop. After `eof`
          // the loop's `ended` guard stops us before we reconnect.
          throw new Error("stream closed");
        },
        onerror(err) {
          throw err;
        },
      });
      return;
    } catch (err) {
      if (signal.aborted || ended) return;
      if (err instanceof FatalStreamError) {
        handlers.onStatus("error", err.message);
        return;
      }
      handlers.onStatus(
        "reconnecting",
        err instanceof Error ? err.message : undefined,
      );
      attempt += 1;
      await sleep(backoffMs(attempt), signal);
    }
  }
}
