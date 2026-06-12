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

// state carries the optional `from` the runs index passes when linking here, so
// the back-target tests can exercise both arrival paths.
function runViewTree(state?: { from: string }) {
  return (
    <MemoryRouter
      initialEntries={[{ pathname: "/applications/app-1/runs/run-1", state }]}
    >
      <Routes>
        <Route
          path="/applications/:appId/runs/:runId"
          element={<WorkflowRunView />}
        />
        <Route path="/applications/:appId" element={<div>application page</div>} />
        <Route path="/runs" element={<div>runs index</div>} />
      </Routes>
    </MemoryRouter>
  );
}

function renderRunView(state?: { from: string }) {
  return render(runViewTree(state));
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

// A deploy run parked at a manual-approval gate on a tofu pair: A (the plan
// unit) succeeded, B (the apply unit, depends on A) is awaiting_approval. The
// snapshot carries the per-unit command the backend expansion stamps, which is
// what pairs the apply step with its plan step. Non-terminal, so the stream
// stays open.
const awaitingDetail = {
  ...runDetail,
  action: "deploy",
  status: "awaiting_approval",
  finished_at: undefined,
  graph: JSON.stringify({
    nodes: [
      {
        id: compA,
        name: "infra · plan",
        type: "terraform",
        config: { command: "plan" },
        depends_on: [],
      },
      {
        id: compB,
        name: "infra · apply",
        type: "terraform",
        config: { command: "apply" },
        depends_on: [compA],
      },
    ],
  }),
  component_runs: [
    { id: "cr-a", component_id: compA, name: "infra · plan", type: "terraform", status: "succeeded" },
    { id: "cr-b", component_id: compB, name: "infra · apply", type: "terraform", status: "awaiting_approval" },
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

  it("shows a component run's preview diff first, with logs on their own tab", async () => {
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
    // A preview run opens on the diff tab.
    expect(await screen.findByText("+ added line")).toBeInTheDocument();
    expect(screen.getByText("changes")).toBeInTheDocument();
    expect(
      screen.queryByText("helm upgrade output here"),
    ).not.toBeInTheDocument();
    // The Logs tab swaps to the full-width log view.
    fireEvent.click(screen.getByRole("button", { name: /^logs$/i }));
    expect(
      await screen.findByText("helm upgrade output here"),
    ).toBeInTheDocument();
  });

  it("expands the panel to fill the view, hiding the DAG", async () => {
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
            has_changes: false,
          },
          error: undefined,
        });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    fireEvent.click(await screen.findByText("release"));
    await screen.findByRole("button", { name: /expand panel/i });
    // Expanding unmounts the DAG (node "apply" disappears), leaving the panel
    // the whole content area; collapsing brings it back.
    fireEvent.click(screen.getByRole("button", { name: /expand panel/i }));
    expect(screen.queryByText("apply")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /collapse panel/i }));
    expect(await screen.findByText("apply")).toBeInTheDocument();
  });

  it("goes back to the application by default", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    fireEvent.click(
      await screen.findByRole("button", { name: /back to application/i }),
    );
    expect(await screen.findByText("application page")).toBeInTheDocument();
  });

  it("goes back to the runs index when the user came from there", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView({ from: "/runs?application=app-1" });
    fireEvent.click(
      await screen.findByRole("button", { name: /back to runs/i }),
    );
    expect(await screen.findByText("runs index")).toBeInTheDocument();
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

  // The component-detail mock for the parked tofu pair: the apply step (cr-b)
  // is parked with no logs of its own; the plan step (cr-a) settled with the
  // plan output the gate is reviewing.
  function mockAwaitingComponentDetails() {
    mockApi.GET.mockImplementation(
      (
        path: string,
        opts?: { params?: { path?: { componentRunId?: string } } },
      ) => {
        if (path === "/api/applications/{id}/runs/{runId}")
          return Promise.resolve({ data: awaitingDetail, error: undefined });
        if (
          path ===
          "/api/applications/{id}/runs/{runId}/components/{componentRunId}"
        ) {
          const id = opts?.params?.path?.componentRunId;
          return Promise.resolve({
            data:
              id === "cr-a"
                ? {
                    id: "cr-a",
                    name: "infra · plan",
                    type: "terraform",
                    status: "succeeded",
                    logs: "tofu plan output",
                  }
                : {
                    id: "cr-b",
                    name: "infra · apply",
                    type: "terraform",
                    status: "awaiting_approval",
                    logs: "",
                  },
            error: undefined,
          });
        }
        return Promise.resolve({ data: undefined, error: undefined });
      },
    );
  }

  it("shows Approve/Reject on a parked step and POSTs the approve endpoint", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockAwaitingComponentDetails();
    mockApi.POST.mockResolvedValue({ data: awaitingDetail, error: undefined });
    renderRunView();
    // Open the parked apply step's panel.
    fireEvent.click(await screen.findByText("infra · apply"));
    const approve = await screen.findByRole("button", { name: /approve/i });
    expect(screen.getByRole("button", { name: /reject/i })).toBeInTheDocument();
    fireEvent.click(approve);
    await waitFor(() =>
      expect(mockApi.POST).toHaveBeenCalledWith(
        "/api/applications/{id}/runs/{runId}/components/{componentRunId}/approve",
        { params: { path: { id: "app-1", runId: "run-1", componentRunId: "cr-b" } } },
      ),
    );
  });

  it("leads with the upstream plan output on a parked tofu apply step", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockAwaitingComponentDetails();
    renderRunView();
    fireEvent.click(await screen.findByText("infra · apply"));
    // The parked apply step opens on the Plan output tab, showing the plan
    // step's logs — the review material for the gate.
    expect(await screen.findByText("tofu plan output")).toBeInTheDocument();
    // The apply step's own (empty) logs sit behind the Logs tab.
    fireEvent.click(screen.getByRole("button", { name: /^logs$/i }));
    expect(
      await screen.findByText("No logs were captured for this step."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /plan output/i }));
    expect(await screen.findByText("tofu plan output")).toBeInTheDocument();
  });

  // A settled deploy run for the tofu pair: both units succeeded, and the
  // apply unit captured the module's outputs. The detail mock parameterizes the
  // outputs so the masking tests can model an editor (value present) and a
  // viewer (sensitive value withheld by the API).
  function mockSettledTofuRun(outputs: Record<string, unknown>) {
    const settled = {
      ...awaitingDetail,
      status: "succeeded",
      finished_at: "2026-06-12T09:05:00Z",
      component_runs: [
        { id: "cr-a", component_id: compA, name: "infra · plan", type: "terraform", status: "succeeded" },
        { id: "cr-b", component_id: compB, name: "infra · apply", type: "terraform", status: "succeeded" },
      ],
    };
    mockApi.GET.mockImplementation(
      (
        path: string,
        opts?: { params?: { path?: { componentRunId?: string } } },
      ) => {
        if (path === "/api/applications/{id}/runs/{runId}")
          return Promise.resolve({ data: settled, error: undefined });
        if (
          path ===
          "/api/applications/{id}/runs/{runId}/components/{componentRunId}"
        ) {
          const id = opts?.params?.path?.componentRunId;
          return Promise.resolve({
            data:
              id === "cr-b"
                ? {
                    id: "cr-b",
                    name: "infra · apply",
                    type: "terraform",
                    status: "succeeded",
                    logs: "apply logs",
                    outputs,
                  }
                : {
                    id: "cr-a",
                    name: "infra · plan",
                    type: "terraform",
                    status: "succeeded",
                    logs: "tofu plan output",
                  },
            error: undefined,
          });
        }
        return Promise.resolve({ data: undefined, error: undefined });
      },
    );
  }

  it("shows captured outputs on a settled tofu apply step, masking sensitive values", async () => {
    mockSettledTofuRun({
      namespace: { value: "customer-a", sensitive: false },
      db_password: { value: "hunter2", sensitive: true },
      subnet_ids: { value: ["a", "b"], sensitive: false },
    });
    renderRunView();
    fireEvent.click(await screen.findByText("infra · apply"));
    fireEvent.click(await screen.findByRole("button", { name: /^outputs$/i }));
    // Non-sensitive values render bare (strings) or as JSON (lists).
    expect(await screen.findByText("customer-a")).toBeInTheDocument();
    expect(screen.getByText('["a","b"]')).toBeInTheDocument();
    // The sensitive value is masked until revealed, then maskable again.
    expect(screen.queryByText("hunter2")).not.toBeInTheDocument();
    expect(screen.getByText("••••••••")).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: /reveal db_password/i }),
    );
    expect(await screen.findByText("hunter2")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /mask db_password/i }));
    expect(screen.queryByText("hunter2")).not.toBeInTheDocument();
  });

  it("offers no reveal when a sensitive output's value was withheld by the API", async () => {
    // Below editor the API drops sensitive values entirely — the entry still
    // lists, masked, but there is nothing to reveal.
    mockSettledTofuRun({ db_password: { sensitive: true } });
    renderRunView();
    fireEvent.click(await screen.findByText("infra · apply"));
    fireEvent.click(await screen.findByRole("button", { name: /^outputs$/i }));
    expect(await screen.findByText("db_password")).toBeInTheDocument();
    expect(screen.getByText("••••••••")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /reveal db_password/i }),
    ).not.toBeInTheDocument();
  });

  it("shows no Outputs tab for a step without captured outputs", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockAwaitingComponentDetails();
    renderRunView();
    fireEvent.click(await screen.findByText("infra · apply"));
    await screen.findByText("tofu plan output");
    expect(
      screen.queryByRole("button", { name: /^outputs$/i }),
    ).not.toBeInTheDocument();
  });

  it("renders a distinct awaiting-approval run status badge", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: awaitingDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    // The humanized label appears (header badge + node label).
    expect(
      (await screen.findAllByText(/awaiting approval/i)).length,
    ).toBeGreaterThanOrEqual(1);
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
    // A preview run opens on the diff tab; switch to the logs tab.
    fireEvent.click(await screen.findByRole("button", { name: /^logs$/i }));
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
    rerender(runViewTree());
    expect(await screen.findByText("final logs captured")).toBeInTheDocument();
    expect(componentCalls).toBe(2);
  });
});
