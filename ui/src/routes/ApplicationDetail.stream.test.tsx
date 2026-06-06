import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationDetail } from "./ApplicationDetail";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "editor",
  }),
}));

// The status stream is the unit under test for the transition; mock the hook so
// the test drives what the stream "delivers" (mirrors DeploymentDetail.test's
// approach of mocking the streaming hook rather than the SSE transport).
vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

const deployingApp = {
  id: "app-1",
  name: "web",
  chart_source: "http_repo",
  status: "deploying",
  sync_status: "unknown",
  target_namespace: "apps",
  target_cluster_id: "c1",
  runner_cluster_id: "c1",
  config: {},
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1"]}>
      <Routes>
        <Route path="/applications/:appId" element={<ApplicationDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  mockStream.mockReset();
  // Default: no streamed value (stream idle).
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: deployingApp, error: undefined });
    if (path === "/api/applications/{id}/deployments")
      return Promise.resolve({ data: [], error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

describe("ApplicationDetail load error branch", () => {
  it("renders the error message when the application can't be loaded", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: undefined, error: { message: "no access" } });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    expect(await screen.findByText("no access")).toBeInTheDocument();
  });

  it("falls back to a default message when the error has none", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: undefined, error: {} });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    expect(
      await screen.findByText("Could not load this application"),
    ).toBeInTheDocument();
  });
});

describe("ApplicationDetail SSE status transition", () => {
  it("subscribes to the status stream while a rollout is in flight", async () => {
    renderDetail();
    await screen.findByRole("heading", { name: "web" });
    // The deploying status opens the stream (enabled=true) for the app's path.
    await waitFor(() =>
      expect(mockStream).toHaveBeenLastCalledWith(
        "/api/applications/app-1/stream",
        true,
      ),
    );
    // The in-flight status badge is shown.
    expect(screen.getByText("deploying")).toBeInTheDocument();
  });

  it("folds a streamed terminal app state into the page", async () => {
    // Load resolves to the in-flight (deploying) row first; then the status
    // stream delivers the terminal (deployed) row, which the page must fold into
    // its displayed state — a new object reference so the fold effect re-runs.
    const { rerender } = renderDetail();
    await screen.findByText("deploying");
    // No Upgrade yet — a deploying app shows Deploy (disabled while in flight).
    expect(
      screen.queryByRole("button", { name: /upgrade/i }),
    ).not.toBeInTheDocument();

    const settled = { ...deployingApp, status: "deployed", sync_status: "synced" };
    mockStream.mockReturnValue({ value: settled, status: "live", error: null });
    rerender(
      <MemoryRouter initialEntries={["/applications/app-1"]}>
        <Routes>
          <Route path="/applications/:appId" element={<ApplicationDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    // The streamed terminal state wins, flipping the badge and the action.
    expect(await screen.findByText("deployed")).toBeInTheDocument();
    expect(screen.queryByText("deploying")).not.toBeInTheDocument();
    expect(
      await screen.findByRole("button", { name: /upgrade/i }),
    ).toBeInTheDocument();
  });

  it("does not open the stream for a settled application", async () => {
    const settled = { ...deployingApp, status: "deployed", sync_status: "synced" };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: settled, error: undefined });
      if (path === "/api/applications/{id}/deployments")
        return Promise.resolve({ data: [], error: undefined });
      if (path === "/api/clusters")
        return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    await screen.findByRole("heading", { name: "web" });
    // The hook is always called, but with enabled=false when nothing is running.
    await waitFor(() =>
      expect(mockStream).toHaveBeenLastCalledWith(
        "/api/applications/app-1/stream",
        false,
      ),
    );
  });

  it("keeps the stream open while a refresh (sync) job is running", async () => {
    const refreshing = {
      ...deployingApp,
      status: "deployed",
      sync_status: "refreshing",
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: refreshing, error: undefined });
      if (path === "/api/applications/{id}/deployments")
        return Promise.resolve({ data: [], error: undefined });
      if (path === "/api/clusters")
        return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    await screen.findByRole("heading", { name: "web" });
    // A refreshing sync_status keeps the status stream open even though the
    // rollout itself has settled.
    await waitFor(() =>
      expect(mockStream).toHaveBeenLastCalledWith(
        "/api/applications/app-1/stream",
        true,
      ),
    );
  });
});
