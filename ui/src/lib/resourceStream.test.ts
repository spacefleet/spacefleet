import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The auth accessors are read on every (re)connect; stub them so a connection
// attempt doesn't depend on a real token/org.
vi.mock("../api/client", () => ({
  authToken: () => Promise.resolve("tok"),
  currentOrgId: () => "org-1",
}));

// fetchEventSource is the transport. We replace it per-test with a fake that
// either drives the onopen/onerror callbacks or throws, so we can exercise the
// reconnect loop in resourceStream itself (which is what's untested) without a
// network.
const fetchEventSource = vi.fn();
vi.mock("@microsoft/fetch-event-source", () => ({
  fetchEventSource: (...args: unknown[]) => fetchEventSource(...args),
}));

import { connectResourceStream, type StreamHandlers } from "./resourceStream";

type FetchOpts = Parameters<typeof import("@microsoft/fetch-event-source").fetchEventSource>[1];

// A no-op handler set; individual tests spy on the fields they care about.
function handlers(): StreamHandlers<unknown> {
  return {
    onSnapshot: vi.fn(),
    onUpsert: vi.fn(),
    onDelete: vi.fn(),
    onStatus: vi.fn(),
  };
}

// flush drains queued microtasks (awaited promises inside the loop) so it
// advances to its next await — fake timers don't tick microtasks. Iterate a few
// times since onopen awaits res.json() (another microtask hop).
async function flush() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

// settle advances past any scheduled backoff and drains the resulting attempt,
// repeated so a chain of reconnect attempts all run to completion.
async function settle(ms = 60_000) {
  for (let i = 0; i < 6; i++) {
    vi.advanceTimersByTime(ms);
    await flush();
  }
}

beforeEach(() => {
  fetchEventSource.mockReset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("connectResourceStream reconnect/backoff", () => {
  it("backs off with growing delays on retriable errors and reconnects", async () => {
    // Every attempt fails with a generic (retriable) error.
    fetchEventSource.mockRejectedValue(new Error("network blip"));

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectResourceStream("/api/x/stream", h, ctrl.signal);

    // First attempt: status "connecting", then the rejection routes to
    // "reconnecting" and schedules a backoff sleep.
    await flush();
    expect(h.onStatus).toHaveBeenNthCalledWith(1, "connecting");
    // The rejection carries its message through to the reconnecting status.
    expect(h.onStatus).toHaveBeenCalledWith("reconnecting", "network blip");
    expect(fetchEventSource).toHaveBeenCalledTimes(1);

    // attempt 1 -> 1000 * 2**1 = 2000ms. Nothing fires before then.
    vi.advanceTimersByTime(1999);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(2);

    // attempt 2 -> 1000 * 2**2 = 4000ms (strictly longer than the first wait).
    vi.advanceTimersByTime(4000);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(3);

    // Stop the loop.
    ctrl.abort();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
  });

  it("caps the backoff delay at the maximum", async () => {
    fetchEventSource.mockRejectedValue(new Error("blip"));

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectResourceStream("/api/x/stream", h, ctrl.signal);
    await flush();

    // Drive several attempts past the 2**4 plateau; each subsequent wait is the
    // 15s cap. Advancing 15s must always yield exactly one more attempt.
    let calls = 1;
    for (let i = 0; i < 6; i++) {
      vi.advanceTimersByTime(15_000);
      await flush();
      calls += 1;
      expect(fetchEventSource).toHaveBeenCalledTimes(calls);
    }

    ctrl.abort();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
  });

  it("resolves immediately without reconnecting once the signal is aborted", async () => {
    fetchEventSource.mockRejectedValue(new Error("blip"));
    const ctrl = new AbortController();
    const h = handlers();
    const done = connectResourceStream("/api/x/stream", h, ctrl.signal);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(1);

    // Abort during the backoff window: the sleep resolves early and the loop
    // exits without another connect.
    ctrl.abort();
    await flush();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
    expect(fetchEventSource).toHaveBeenCalledTimes(1);
  });
});

describe("connectResourceStream fatal vs retriable classification", () => {
  // onopen rejecting a 403 must be fatal: surface "error" and stop the loop.
  async function runStatus(status: number): Promise<{
    h: StreamHandlers<unknown>;
    calls: number;
  }> {
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      // A non-event-stream error response with a JSON body, as the backend sends.
      await opts.onopen!({
        ok: false,
        status,
        headers: { get: () => "application/json" },
        json: async () => ({ message: `boom ${status}` }),
      } as unknown as Response);
    });

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectResourceStream("/api/x/stream", h, ctrl.signal);
    await flush();
    // Let any scheduled retry fire (it must NOT, for fatal codes).
    await settle();
    ctrl.abort();
    await settle();
    await done;
    return { h, calls: fetchEventSource.mock.calls.length };
  }

  it.each([400, 403, 404])(
    "treats %i as fatal: errors and stops reconnecting",
    async (status) => {
      const { h, calls } = await runStatus(status);
      expect(h.onStatus).toHaveBeenCalledWith("error", `boom ${status}`);
      // Exactly one connect — no reconnect was scheduled.
      expect(calls).toBe(1);
    },
  );

  it.each([401, 500, 502])(
    "treats %i as retriable: reconnects rather than erroring",
    async (status) => {
      const { h, calls } = await runStatus(status);
      // Never surfaced as a fatal error...
      expect(h.onStatus).not.toHaveBeenCalledWith("error", expect.anything());
      // ...and the loop kept reconnecting (more than the first attempt).
      expect(h.onStatus).toHaveBeenCalledWith("reconnecting", `boom ${status}`);
      expect(calls).toBeGreaterThan(1);
    },
  );

  it("marks the stream live and resets backoff on a clean open", async () => {
    let openedAttempts = 0;
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      openedAttempts += 1;
      // First open succeeds (live); then we throw to force a reconnect, and the
      // next open succeeds too — proving the loop keeps running after live.
      await opts.onopen!({
        ok: true,
        status: 200,
        headers: { get: () => "text/event-stream" },
        json: async () => null,
      } as unknown as Response);
      throw new Error("dropped");
    });

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectResourceStream("/api/x/stream", h, ctrl.signal);
    await flush();
    expect(h.onStatus).toHaveBeenCalledWith("live");
    // After live, the throw routes to reconnecting (retriable), not error.
    expect(h.onStatus).toHaveBeenCalledWith("reconnecting", "dropped");
    expect(h.onStatus).not.toHaveBeenCalledWith("error", expect.anything());

    // A reset attempt counter (live set attempt=0) means the next wait is the
    // smallest (2000ms), so one more connect fires after 2s.
    vi.advanceTimersByTime(2000);
    await flush();
    expect(openedAttempts).toBeGreaterThan(1);

    ctrl.abort();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
  });

  it("routes snapshot/added/modified/deleted events to the right handler", async () => {
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      await opts.onopen!({
        ok: true,
        status: 200,
        headers: { get: () => "text/event-stream" },
        json: async () => null,
      } as unknown as Response);
      opts.onmessage!({ event: "snapshot", data: JSON.stringify([{ id: "a" }]) } as never);
      opts.onmessage!({ event: "added", data: JSON.stringify({ id: "b" }) } as never);
      opts.onmessage!({ event: "modified", data: JSON.stringify({ id: "c" }) } as never);
      opts.onmessage!({ event: "deleted", data: JSON.stringify({ id: "d" }) } as never);
      // No event name / no data -> ignored.
      opts.onmessage!({ event: "", data: "" } as never);
    });

    const ctrl = new AbortController();
    const h = handlers();
    // Don't await: the fake never resolves the connect, so abort to finish.
    connectResourceStream("/api/x/stream", h, ctrl.signal);
    await flush();

    expect(h.onSnapshot).toHaveBeenCalledWith([{ id: "a" }]);
    expect(h.onUpsert).toHaveBeenCalledWith({ id: "b" });
    expect(h.onUpsert).toHaveBeenCalledWith({ id: "c" });
    expect(h.onDelete).toHaveBeenCalledWith({ id: "d" });
    expect(h.onUpsert).toHaveBeenCalledTimes(2);
    ctrl.abort();
  });
});
