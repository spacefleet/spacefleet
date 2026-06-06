import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../api/client", () => ({
  authToken: () => Promise.resolve("tok"),
  currentOrgId: () => "org-1",
}));

const fetchEventSource = vi.fn();
vi.mock("@microsoft/fetch-event-source", () => ({
  fetchEventSource: (...args: unknown[]) => fetchEventSource(...args),
}));

import { connectObjectStream, type ObjectStreamHandlers } from "./objectStream";

type FetchOpts = Parameters<typeof import("@microsoft/fetch-event-source").fetchEventSource>[1];

function handlers(): ObjectStreamHandlers<unknown> {
  return { onUpdate: vi.fn(), onStatus: vi.fn() };
}

// flush drains queued microtasks (awaited promises inside the loop) so it
// advances to its next await; fake timers don't tick microtasks. Iterate a few
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

describe("connectObjectStream reconnect/backoff", () => {
  it("backs off with growing delays on retriable errors", async () => {
    fetchEventSource.mockRejectedValue(new Error("blip"));

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectObjectStream("/api/app/stream", h, ctrl.signal);

    await flush();
    expect(h.onStatus).toHaveBeenNthCalledWith(1, "connecting");
    expect(h.onStatus).toHaveBeenCalledWith("reconnecting", "blip");
    expect(fetchEventSource).toHaveBeenCalledTimes(1);

    // attempt 1 -> 2000ms.
    vi.advanceTimersByTime(1999);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(2);

    // attempt 2 -> 4000ms (longer than the first wait).
    vi.advanceTimersByTime(4000);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(3);

    ctrl.abort();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
  });

  it("caps the backoff delay at the maximum", async () => {
    fetchEventSource.mockRejectedValue(new Error("blip"));
    const ctrl = new AbortController();
    const h = handlers();
    const done = connectObjectStream("/api/app/stream", h, ctrl.signal);
    await flush();

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

  it("stops without reconnecting once aborted during backoff", async () => {
    fetchEventSource.mockRejectedValue(new Error("blip"));
    const ctrl = new AbortController();
    const h = handlers();
    const done = connectObjectStream("/api/app/stream", h, ctrl.signal);
    await flush();
    expect(fetchEventSource).toHaveBeenCalledTimes(1);

    ctrl.abort();
    await flush();
    vi.advanceTimersByTime(60_000);
    await flush();
    await done;
    expect(fetchEventSource).toHaveBeenCalledTimes(1);
  });
});

describe("connectObjectStream fatal vs retriable classification", () => {
  async function runStatus(status: number) {
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      await opts.onopen!({
        ok: false,
        status,
        headers: { get: () => "application/json" },
        json: async () => ({ message: `boom ${status}` }),
      } as unknown as Response);
    });

    const ctrl = new AbortController();
    const h = handlers();
    const done = connectObjectStream("/api/app/stream", h, ctrl.signal);
    await flush();
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
      expect(calls).toBe(1);
    },
  );

  it.each([401, 500, 502])(
    "treats %i as retriable: reconnects rather than erroring",
    async (status) => {
      const { h, calls } = await runStatus(status);
      expect(h.onStatus).not.toHaveBeenCalledWith("error", expect.anything());
      expect(h.onStatus).toHaveBeenCalledWith("reconnecting", `boom ${status}`);
      expect(calls).toBeGreaterThan(1);
    },
  );

  it("falls back to a status-coded message when the body has none", async () => {
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      await opts.onopen!({
        ok: false,
        status: 404,
        headers: { get: () => "application/json" },
        // Body isn't JSON -> json() rejects -> message falls back.
        json: async () => {
          throw new Error("not json");
        },
      } as unknown as Response);
    });
    const ctrl = new AbortController();
    const h = handlers();
    const done = connectObjectStream("/api/app/stream", h, ctrl.signal);
    await flush();
    expect(h.onStatus).toHaveBeenCalledWith("error", "stream failed (404)");
    ctrl.abort();
    await settle();
    await done;
  });
});

describe("connectObjectStream value delivery", () => {
  it("delivers any data-bearing event as the latest value and ignores empty data", async () => {
    fetchEventSource.mockImplementation(async (_path: string, opts: FetchOpts) => {
      await opts.onopen!({
        ok: true,
        status: 200,
        headers: { get: () => "text/event-stream" },
        json: async () => null,
      } as unknown as Response);
      // Event name is irrelevant for the object stream — any data is the value.
      opts.onmessage!({ event: "status", data: JSON.stringify({ phase: "running" }) } as never);
      opts.onmessage!({ event: "modified", data: JSON.stringify({ phase: "done" }) } as never);
      // No data -> ignored.
      opts.onmessage!({ event: "ping", data: "" } as never);
    });

    const ctrl = new AbortController();
    const h = handlers();
    connectObjectStream("/api/app/stream", h, ctrl.signal);
    await flush();

    expect(h.onStatus).toHaveBeenCalledWith("live");
    expect(h.onUpdate).toHaveBeenNthCalledWith(1, { phase: "running" });
    expect(h.onUpdate).toHaveBeenNthCalledWith(2, { phase: "done" });
    expect(h.onUpdate).toHaveBeenCalledTimes(2);
    ctrl.abort();
  });
});
