import { afterEach, describe, expect, it, vi } from "vitest";
import { api, setAuthTokenProvider, setUnauthorizedHandler } from "./client";

// Answer every request with the given status, standing in for the backend.
function respondWith(status: number) {
  return async () =>
    new Response(JSON.stringify({ code: "x", message: "x" }), {
      status,
      headers: { "Content-Type": "application/json" },
    });
}

// An absolute baseUrl so the client can build a Request outside a browser.
function getMe(status: number) {
  return api.GET("/api/me", {
    baseUrl: "http://localhost",
    fetch: respondWith(status),
  });
}

afterEach(() => {
  setAuthTokenProvider(null);
  setUnauthorizedHandler(null);
});

describe("api client 401 handling", () => {
  it("reports the rejected token when the backend answers 401", async () => {
    const onUnauthorized = vi.fn();
    setAuthTokenProvider(async () => "stale-token");
    setUnauthorizedHandler(onUnauthorized);

    await getMe(401);

    expect(onUnauthorized).toHaveBeenCalledWith("stale-token");
  });

  it("ignores a 401 for a request sent without a token", async () => {
    const onUnauthorized = vi.fn();
    setAuthTokenProvider(async () => null);
    setUnauthorizedHandler(onUnauthorized);

    await getMe(401);

    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("ignores non-401 failures", async () => {
    const onUnauthorized = vi.fn();
    setAuthTokenProvider(async () => "good-token");
    setUnauthorizedHandler(onUnauthorized);

    await getMe(403);

    expect(onUnauthorized).not.toHaveBeenCalled();
  });
});
