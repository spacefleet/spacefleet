import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationDetail } from "./ApplicationDetail";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(),
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "editor",
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

const app = {
  id: "app-1",
  name: "web",
  imported: false,
  runner_cluster_id: "c1",
  created_at: "2026-06-03T09:00:00Z",
  updated_at: "2026-06-03T10:00:00Z",
};

const run = {
  id: "run-2",
  application_id: "app-1",
  action: "deploy",
  status: "succeeded",
  created_at: "2026-06-03T10:00:00Z",
  finished_at: "2026-06-03T10:01:00Z",
};

// A two-component workflow for the at-a-glance overview card.
const workflow = {
  components: [
    {
      id: "11111111-1111-1111-1111-111111111111",
      name: "release",
      type: "helm",
      config: {},
      depends_on: [],
      continue_on_failure: false,
      target_namespace: "",
      position: { x: 100, y: 100 },
    },
    {
      id: "22222222-2222-2222-2222-222222222222",
      name: "apply",
      type: "manifest",
      config: {},
      depends_on: ["11111111-1111-1111-1111-111111111111"],
      continue_on_failure: false,
      target_namespace: "",
      position: { x: 100, y: 300 },
    },
  ],
  groups: [],
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1"]}>
      <Routes>
        <Route path="/applications/:appId" element={<ApplicationDetail />} />
        <Route
          path="/applications/:appId/workflow"
          element={<div>workflow canvas</div>}
        />
        <Route
          path="/applications/:appId/runs/:runId"
          element={<div>run view</div>}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  mockStream.mockReset();
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: app, error: undefined });
    if (path === "/api/applications/{id}/runs")
      return Promise.resolve({ data: { runs: [run] }, error: undefined });
    if (path === "/api/applications/{id}/workflow")
      return Promise.resolve({ data: workflow, error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({
        data: [{ id: "c1", name: "prod" }],
        error: undefined,
      });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

describe("ApplicationDetail overview", () => {
  it("shows the runner and the latest run status", async () => {
    renderDetail();
    expect(await screen.findByText("web")).toBeInTheDocument();
    // The runner cluster id resolves to its name.
    expect((await screen.findAllByText("prod")).length).toBeGreaterThan(0);
    // The latest run's action + status are shown.
    expect(screen.getByText("deploy")).toBeInTheDocument();
    expect(screen.getByText("succeeded")).toBeInTheDocument();
  });

  it("links to the workflow builder", async () => {
    renderDetail();
    await userEvent.click(
      await screen.findByRole("button", { name: /edit workflow/i }),
    );
    expect(await screen.findByText("workflow canvas")).toBeInTheDocument();
  });

  it("shows the workflow overview's component nodes", async () => {
    renderDetail();
    // Both components from the saved workflow render in the overview DAG.
    expect(await screen.findByText("release")).toBeInTheDocument();
    expect(screen.getByText("apply")).toBeInTheDocument();
  });

  it("starts a deploy run and navigates to the run view", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^deploy$/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: false } }),
    );
  });

  it("sends force=true on deploy when the Force workload roll toggle is on", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(
      screen.getByRole("checkbox", { name: /force workload roll/i }),
    );
    await userEvent.click(screen.getByRole("button", { name: /^deploy$/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: true } }),
    );
  });

  it("shows an in-progress message on a 409", async () => {
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^deploy$/i }));
    expect(
      await screen.findByText(/a run is already in progress/i),
    ).toBeInTheDocument();
  });

  it("opens the run view from the latest run", async () => {
    renderDetail();
    await userEvent.click(await screen.findByText("deploy"));
    expect(await screen.findByText("run view")).toBeInTheDocument();
  });

  it("does not open the run stream when the latest run is terminal", async () => {
    renderDetail();
    await screen.findByText("succeeded");
    // succeeded is terminal — the stream is gated off (enabled=false).
    expect(mockStream).toHaveBeenLastCalledWith(
      "/api/applications/app-1/runs/run-2/stream",
      false,
    );
  });

  it("follows the run stream and updates the badge for a non-terminal run", async () => {
    const running = { ...run, status: "running", finished_at: undefined };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/runs")
        return Promise.resolve({ data: { runs: [running] }, error: undefined });
      if (path === "/api/clusters")
        return Promise.resolve({
          data: [{ id: "c1", name: "prod" }],
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    // The stream reports the run has since succeeded.
    mockStream.mockReturnValue({
      value: { ...running, status: "succeeded" },
      status: "live",
      error: null,
    });
    renderDetail();
    // The badge reflects the streamed terminal status, not the fetched "running".
    expect(await screen.findByText("succeeded")).toBeInTheDocument();
    // The stream was enabled while in flight.
    expect(mockStream).toHaveBeenCalledWith(
      "/api/applications/app-1/runs/run-2/stream",
      true,
    );
  });

  it("shows the empty state when there are no runs", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: app, error: undefined });
      if (path === "/api/applications/{id}/runs")
        return Promise.resolve({ data: { runs: [] }, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderDetail();
    expect(await screen.findByText(/No runs yet/)).toBeInTheDocument();
  });
});
