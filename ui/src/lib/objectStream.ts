import { fetchEventSource } from "@microsoft/fetch-event-source";
import { authToken, currentOrgId } from "../api/client";
import { type StreamStatus } from "./resourceStream";

// Single-object counterpart to resourceStream: where connectResourceStream
// maintains a live *set* (snapshot + add/modified/deleted deltas), this delivers
// successive states of one object. It's the transport for the Tekton install
// status stream (`status` events) and a TaskRun status stream (`snapshot` +
// `modified` events). Any event carrying data is treated as the latest value, so
// the caller doesn't care which event name the server used.
//
// It shares resourceStream's auth + reconnect model: the same Authorization +
// X-Organization-ID headers, our own backoff loop (so each reconnect re-reads a
// fresh token), and the same fatal-vs-retriable classification.

export interface ObjectStreamHandlers<T> {
  onUpdate: (value: T) => void;
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

// connectObjectStream holds a live connection to a single-object stream,
// reconnecting with backoff until `signal` aborts.
export async function connectObjectStream<T>(
  path: string,
  handlers: ObjectStreamHandlers<T>,
  signal: AbortSignal,
): Promise<void> {
  let attempt = 0;
  while (!signal.aborted) {
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
        async onopen(res) {
          const contentType = res.headers.get("content-type") ?? "";
          if (res.ok && contentType.includes("text/event-stream")) {
            attempt = 0;
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
          if (!msg.data) return;
          handlers.onUpdate(JSON.parse(msg.data) as T);
        },
        onclose() {
          // A clean server close (terminal state or the token-expiry cap) ends
          // the stream; surface it so our loop decides whether to reconnect.
          throw new Error("stream closed");
        },
        onerror(err) {
          throw err;
        },
      });
      return;
    } catch (err) {
      if (signal.aborted) return;
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
