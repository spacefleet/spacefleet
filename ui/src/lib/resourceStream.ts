import { fetchEventSource } from "@microsoft/fetch-event-source";
import { authToken, currentOrgId } from "../api/client";

// Reusable client for the backend's Server-Sent Events resource streams (cluster
// nodes today; pods/workloads next). It consumes SSE over fetch — not the native
// EventSource — so it can send the same Authorization + X-Organization-ID headers
// the rest of the API uses (EventSource can't set headers), keeping auth on one
// model with no token in the URL.
//
// A stream delivers a `snapshot` event (full T[]) followed by `added`/`modified`/
// `deleted` deltas (single T). The server caps each connection to the token's
// lifetime; we own reconnection so every (re)connect re-reads a fresh token.

export type StreamStatus = "connecting" | "live" | "reconnecting" | "error";

export interface StreamHandlers<T> {
  onSnapshot: (items: T[]) => void;
  onUpsert: (item: T) => void;
  onDelete: (item: T) => void;
  onStatus: (status: StreamStatus, error?: string) => void;
}

// FatalStreamError marks a failure that won't fix itself on retry (e.g. the
// caller isn't a member of the org, or the cluster doesn't exist), so the loop
// stops rather than reconnecting forever.
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

// connectResourceStream holds a live connection to a streaming endpoint,
// reconnecting with backoff until `signal` aborts. Resource-agnostic: it parses
// each event's JSON and routes it to the matching handler.
export async function connectResourceStream<T>(
  path: string,
  handlers: StreamHandlers<T>,
  signal: AbortSignal,
): Promise<void> {
  let attempt = 0;
  while (!signal.aborted) {
    handlers.onStatus(attempt === 0 ? "connecting" : "reconnecting");

    // Re-read the token on every attempt: react-oidc-context keeps it
    // refreshed, so a reconnect after the server's expiry cap uses a fresh one.
    const token = await authToken();
    const orgId = currentOrgId();
    const headers: Record<string, string> = { Accept: "text/event-stream" };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (orgId) headers["X-Organization-ID"] = orgId;

    try {
      await fetchEventSource(path, {
        headers,
        signal,
        // Keep streaming while the tab/window is backgrounded — the whole point
        // is to watch the list without it being the focused window.
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
          // 400/403/404 won't resolve on retry; everything else (401 expired
          // token, 502 cluster temporarily unreachable, 5xx) is retriable.
          if ([400, 403, 404].includes(res.status)) {
            throw new FatalStreamError(message);
          }
          throw new Error(message);
        },
        onmessage(msg) {
          if (!msg.event || !msg.data) return;
          const data = JSON.parse(msg.data);
          switch (msg.event) {
            case "snapshot":
              handlers.onSnapshot(data as T[]);
              break;
            case "added":
            case "modified":
              handlers.onUpsert(data as T);
              break;
            case "deleted":
              handlers.onDelete(data as T);
              break;
          }
        },
        onclose() {
          // A clean server close (e.g. the token-expiry lifetime cap) should
          // reconnect, so surface it as a retriable error to our loop.
          throw new Error("stream closed");
        },
        onerror(err) {
          // Always rethrow: this disables fetchEventSource's own retry so our
          // loop owns reconnection (and the per-attempt token refresh).
          throw err;
        },
      });
      // Resolved without throwing means the signal aborted — stop.
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
