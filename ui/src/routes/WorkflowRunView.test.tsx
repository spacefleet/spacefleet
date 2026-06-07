import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowRunView } from "./WorkflowRunView";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: "editor" }),
}));

vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

const compA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const compB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";

// A preview run: A succeeded, B (depends on A) failed. The snapshot graph
// carries the node names/types and the edge A->B.
const runDetail = {
  id: "run-1",
  application_id: "app-1",
  action: "preview",
  status: "failed",
  created_at: "2026-06-03T09:00:00Z",
  finished_at: "2026-06-03T09:05:00Z",
  graph: JSON.stringify({
    nodes: [
      { id: compA, name: "release", type: "helm", depends_on: [] },
      { id: compB, name: "apply", type: "manifest", depends_on: [compA] },
    ],
  }),
  component_runs: [
    { id: "cr-a", component_id: compA, name: "release", type: "helm", status: "succeeded" },
    { id: "cr-b", component_id: compB, name: "apply", type: "manifest", status: "failed" },
  ],
};

function renderRunView() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/runs/run-1"]}>
      <Routes>
        <Route
          path="/applications/:appId/runs/:runId"
          element={<WorkflowRunView />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

// A still-running preview: A succeeded, B is running. Non-terminal, so the
// stream stays open.
const runningDetail = {
  ...runDetail,
  status: "running",
  finished_at: undefined,
  component_runs: [
    { id: "cr-a", component_id: compA, name: "release", type: "helm", status: "succeeded" },
    { id: "cr-b", component_id: compB, name: "apply", type: "manifest", status: "running" },
  ],
};

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockStream.mockReset();
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
});

describe("WorkflowRunView", () => {
  it("renders snapshot nodes colored by their component-run status", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    // Both snapshot nodes render with their names.
    expect(await screen.findByText("release")).toBeInTheDocument();
    expect(screen.getByText("apply")).toBeInTheDocument();
    // Their statuses surface on the node (lowercase status label).
    expect(screen.getByText("succeeded")).toBeInTheDocument();
    // "failed" appears on both the run header badge and node B's status label.
    expect(screen.getAllByText("failed").length).toBeGreaterThanOrEqual(2);
  });

  it("does not open the stream for a terminal run", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    await screen.findByText("release");
    // failed is terminal — the hook is called with enabled=false.
    expect(mockStream).toHaveBeenLastCalledWith(
      "/api/applications/app-1/runs/run-1/stream",
      false,
    );
  });

  it("shows a component run's logs and preview diff on node click", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      if (
        path === "/api/applications/{id}/runs/{runId}/components/{componentRunId}"
      )
        return Promise.resolve({
          data: {
            id: "cr-a",
            name: "release",
            type: "helm",
            status: "succeeded",
            logs: "helm upgrade output here",
            diff: "+ added line",
            has_changes: true,
          },
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    // fireEvent.click dispatches only the click (no mousedown), avoiding
    // React Flow's d3-zoom drag handler which crashes in jsdom.
    fireEvent.click(await screen.findByText("release"));
    // The detail panel fetched the component run and shows its logs + diff.
    expect(await screen.findByText("helm upgrade output here")).toBeInTheDocument();
    expect(screen.getByText("+ added line")).toBeInTheDocument();
    expect(screen.getByText("changes")).toBeInTheDocument();
  });

  it("shows a Cancel run button only while in flight and POSTs cancel", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runningDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    mockApi.POST.mockResolvedValue({
      data: { ...runningDetail, status: "failed" },
      error: undefined,
    });
    renderRunView();
    await screen.findByText("release");
    const btn = await screen.findByRole("button", { name: /cancel run/i });
    fireEvent.click(btn);
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications/{id}/runs/{runId}/cancel",
        { params: { path: { id: "app-1", runId: "run-1" } } },
      ),
    );
  });

  it("hides the Cancel run button for a terminal run", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    await screen.findByText("release");
    expect(
      screen.queryByRole("button", { name: /cancel run/i }),
    ).not.toBeInTheDocument();
  });

  it("re-fetches the component detail when its live status goes terminal", async () => {
    // Start running; the panel opens on the running step B and fetches detail
    // (empty logs). When the stream reports B succeeded, the live status changes
    // and the panel must re-fetch — now with logs.
    let componentCalls = 0;
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runningDetail, error: undefined });
      if (
        path === "/api/applications/{id}/runs/{runId}/components/{componentRunId}"
      ) {
        componentCalls += 1;
        return Promise.resolve({
          data: {
            id: "cr-b",
            name: "apply",
            type: "manifest",
            status: componentCalls === 1 ? "running" : "succeeded",
            logs: componentCalls === 1 ? "" : "final logs captured",
            has_changes: false,
          },
          error: undefined,
        });
      }
      return Promise.resolve({ data: undefined, error: undefined });
    });
    const { rerender } = renderRunView();
    fireEvent.click(await screen.findByText("apply"));
    // First fetch: running, no logs.
    await screen.findByText("No logs were captured for this step.");
    expect(componentCalls).toBe(1);

    // The stream now reports B succeeded; folding it changes the panel's live
    // status, which re-runs the fetch effect.
    const settled = {
      ...runningDetail,
      status: "partial",
      component_runs: [
        runningDetail.component_runs[0],
        { ...runningDetail.component_runs[1], status: "succeeded" },
      ],
    };
    mockStream.mockReturnValue({ value: settled, status: "live", error: null });
    rerender(
      <MemoryRouter initialEntries={["/applications/app-1/runs/run-1"]}>
        <Routes>
          <Route
            path="/applications/:appId/runs/:runId"
            element={<WorkflowRunView />}
          />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("final logs captured")).toBeInTheDocument();
    expect(componentCalls).toBe(2);
  });
});
