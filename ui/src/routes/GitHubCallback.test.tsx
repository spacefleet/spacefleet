import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GitHubCallback } from "./GitHubCallback";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { POST: vi.fn() },
}));

const mockApi = api as unknown as { POST: ReturnType<typeof vi.fn> };

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

beforeEach(() => {
  mockApi.POST.mockReset();
});

describe("GitHubCallback", () => {
  it("posts the installation id + state and routes to the admin page", async () => {
    mockApi.POST.mockResolvedValue({ data: {}, error: undefined });
    renderAt("?installation_id=12345&setup_action=install&state=abc");

    await waitFor(() => expect(mockApi.POST).toHaveBeenCalled());
    const [path, opts] = mockApi.POST.mock.calls[0];
    expect(path).toBe("/api/github/installations");
    expect(opts.body).toEqual({ installation_id: 12345, state: "abc" });
    expect(await screen.findByText("GitHub admin page")).toBeInTheDocument();
  });

  it("shows an error when GitHub params are missing", async () => {
    renderAt("?state=abc");
    expect(
      await screen.findByText(/missing installation details/i),
    ).toBeInTheDocument();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("surfaces a server error without navigating", async () => {
    mockApi.POST.mockResolvedValue({
      error: { message: "invalid or expired connect state" },
    });
    renderAt("?installation_id=7&state=stale");
    expect(
      await screen.findByText(/invalid or expired connect state/i),
    ).toBeInTheDocument();
  });
});
