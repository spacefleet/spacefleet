import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GitHubCallback } from "./GitHubCallback";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};

function renderAt(search: string) {
  return render(
    <MemoryRouter initialEntries={[`/github/callback${search}`]}>
      <Routes>
        <Route path="/github/callback" element={<GitHubCallback />} />
        <Route path="/admin/github" element={<div>GitHub admin page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const realLocation = window.location;

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
});

afterEach(() => {
  Object.defineProperty(window, "location", {
    configurable: true,
    value: realLocation,
  });
});

describe("GitHubCallback", () => {
  it("continues a setup-URL install to GitHub's authorize page", async () => {
    mockApi.GET.mockResolvedValue({
      data: { url: "https://github.com/login/oauth/authorize?state=bound" },
      error: undefined,
    });
    // jsdom's window.location isn't assignable by default; stub a setter.
    const hrefSpy = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { set href(v: string) { hrefSpy(v); } },
    });
    renderAt("?installation_id=12345&setup_action=install&state=connect");

    await waitFor(() =>
      expect(hrefSpy).toHaveBeenCalledWith(
        "https://github.com/login/oauth/authorize?state=bound",
      ),
    );
    expect(mockApi.GET).toHaveBeenCalledWith(
      "/api/github/installations/authorize-url",
      { params: { query: { installation_id: 12345, state: "connect" } } },
    );
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("records the installation when GitHub returns from the authorize step", async () => {
    mockApi.POST.mockResolvedValue({ data: {}, error: undefined });
    renderAt("?code=oauth-code&state=bound");

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    const [path, opts] = mockApi.POST.mock.calls[0];
    expect(path).toBe("/api/github/installations");
    expect(opts.body).toEqual({ state: "bound", code: "oauth-code" });
    expect(await screen.findByText("GitHub admin page")).toBeInTheDocument();
  });

  it("records an install from an App that authorizes during installation", async () => {
    mockApi.POST.mockResolvedValue({ data: {}, error: undefined });
    renderAt("?code=oauth-code&installation_id=12345&setup_action=install&state=abc");

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    expect(mockApi.POST.mock.calls[0][1].body).toEqual({
      installation_id: 12345,
      state: "abc",
      code: "oauth-code",
    });
    expect(await screen.findByText("GitHub admin page")).toBeInTheDocument();
  });

  it("returns to the admin page after an installation update", async () => {
    renderAt("?installation_id=12345&setup_action=update");
    expect(await screen.findByText("GitHub admin page")).toBeInTheDocument();
    expect(mockApi.GET).not.toHaveBeenCalled();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("explains an installation awaiting an owner's approval", async () => {
    renderAt("?setup_action=request&state=abc");
    expect(
      await screen.findByText(/asked an owner of the account to approve/i),
    ).toBeInTheDocument();
    expect(mockApi.GET).not.toHaveBeenCalled();
  });

  it("refuses an install that wasn't started from Spacefleet", async () => {
    // No state: the install began on GitHub, not from Connect.
    renderAt("?installation_id=12345&setup_action=install");
    expect(
      await screen.findByText(/missing installation details/i),
    ).toBeInTheDocument();
    expect(mockApi.GET).not.toHaveBeenCalled();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("rejects a malformed installation id", async () => {
    renderAt("?installation_id=abc&setup_action=install&state=s");
    expect(await screen.findByText(/invalid installation id/i)).toBeInTheDocument();
  });

  it("surfaces an authorize-url error", async () => {
    mockApi.GET.mockResolvedValue({
      error: { message: "invalid or expired connect state" },
    });
    renderAt("?installation_id=7&setup_action=install&state=stale");
    expect(
      await screen.findByText(/invalid or expired connect state/i),
    ).toBeInTheDocument();
  });

  it("surfaces a server error without navigating", async () => {
    mockApi.POST.mockResolvedValue({
      error: { message: "invalid or expired connect state" },
    });
    renderAt("?code=oauth-code&state=stale");
    expect(
      await screen.findByText(/invalid or expired connect state/i),
    ).toBeInTheDocument();
  });
});
