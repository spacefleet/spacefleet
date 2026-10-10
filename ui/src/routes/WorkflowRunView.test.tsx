import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowRunView } from "./WorkflowRunView";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";
import { usePodLogs } from "../lib/usePodLogs";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: "editor" }),
}));

vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(),
}));

vi.mock("../lib/usePodLogs", () => ({
  usePodLogs: vi.fn(() => ({ lines: [], status: "idle", ended: false, error: null })),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;
const mockPodLogs = usePodLogs as unknown as ReturnType<typeof vi.fn>;

const compA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const compB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";

// A preview run: A succeeded, B (in the next stage) failed. The snapshot graph
// carries the step names/types and the edge A->B; the server's stage summary
// carries the stages the rail lists.
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
  stages: [
    {
      name: "Charts",
      status: "succeeded",
      components: [
        { component_id: compA, name: "release", type: "helm", status: "succeeded", component_run_ids: ["cr-a"] },
      ],
    },
    {
      name: "Manifests",
      status: "failed",
      components: [
        { component_id: compB, name: "apply", type: "manifest", status: "failed", component_run_ids: ["cr-b"] },
      ],
    },
  ],
};

// LocationProbe prints the current search so a test can read the ?step= param.
function LocationProbe() {
  return <p data-testid="location-search">{useLocation().search}</p>;
}

// state carries the optional `from` the runs index passes when linking here, so
// the breadcrumb tests can exercise both arrival paths.
function runViewTree(state?: { from: string }, search = "") {
  return (
    <MemoryRouter
      initialEntries={[{ pathname: "/applications/app-1/runs/run-1", search, state }]}
    >
      <Routes>
        <Route
          path="/applications/:appId/runs/:runId"
          element={
            <>
              <WorkflowRunView />
              <LocationProbe />
            </>
          }
        />
        <Route path="/applications/:appId" element={<div>application page</div>} />
        <Route path="/applications" element={<div>applications list</div>} />
        <Route
          path="/runs"
          element={
            <>
              <div>runs index</div>
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>
  );
}

function renderRunView(state?: { from: string }, search?: string) {
  return render(runViewTree(state, search));
}

// rail is the run's stage → component → step list; pane the selected step.
function rail() {
  return screen.getByRole("navigation", { name: "Run steps" });
}
function pane() {
  return screen.getByRole("region", { name: "Selected step" });
}

// clickStep selects a rail row by its leading text (a component's name, or an
// OpenTofu step's "plan"/"apply").
async function clickStep(label: string) {
  await screen.findByRole("navigation", { name: "Run steps" });
  const re = new RegExp(`^${label}`);
  fireEvent.click(within(rail()).getByRole("button", { name: re }));
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
  stages: [
    runDetail.stages[0],
    {
      name: "Manifests",
      status: "running",
      components: [
        { component_id: compB, name: "apply", type: "manifest", status: "running", component_run_ids: ["cr-b"] },
      ],
    },
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
  stages: [
    {
      name: "Infrastructure",
      status: "awaiting_approval",
      components: [
        { component_id: compA, name: "infra", type: "terraform", status: "awaiting_approval", component_run_ids: ["cr-a", "cr-b"] },
      ],
    },
  ],
};

// tofuStages is the stage summary of a one-step OpenTofu run (a drift check, a
// state operation, a blocked plan): one component with its single step.
function tofuStages(status: string) {
  return [
    {
      name: "Infrastructure",
      status,
      components: [
        { component_id: compA, name: "infra", type: "terraform", status, component_run_ids: ["cr-a"] },
      ],
    },
  ];
}

beforeEach(() => {
  mockPodLogs.mockReset();
  mockPodLogs.mockImplementation(() => ({ lines: [], status: "idle", ended: false, error: null }));
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
  mockStream.mockReset();
  mockStream.mockReturnValue({ value: null, status: "connecting", error: null });
});

describe("WorkflowRunView", () => {
  it("lists the run's stages in order, each with its components", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    await screen.findByRole("navigation", { name: "Run steps" });
    const charts = within(screen.getByRole("region", { name: "Stage Charts" }));
    expect(charts.getByText("release")).toBeInTheDocument();
    const manifests = within(screen.getByRole("region", { name: "Stage Manifests" }));
    expect(manifests.getByText("apply")).toBeInTheDocument();
    // Stages list in run order.
    const headings = within(rail()).getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Charts", "Manifests"]);
  });

  it("opens on the failed step when none is chosen, without touching the URL", async () => {
    mockApi.GET.mockImplementation(
      (path: string, opts?: { params?: { path?: { componentRunId?: string } } }) => {
        if (path === "/api/applications/{id}/runs/{runId}")
          return Promise.resolve({ data: runDetail, error: undefined });
        const id = opts?.params?.path?.componentRunId;
        return Promise.resolve({
          data: { id, name: id === "cr-b" ? "apply" : "release", type: "helm", status: "failed", diff: `diff of ${id}`, has_changes: true },
          error: undefined,
        });
      },
    );
    renderRunView();
    const selected = await screen.findByRole("region", { name: "Selected step" });
    // A preview run's pane leads with the step's diff.
    expect(await within(selected).findByText("diff of cr-b")).toBeInTheDocument();
    expect(screen.getByTestId("location-search")).toHaveTextContent("");
  });

  it("records a chosen step in the URL, and a linked step opens directly", async () => {
    mockApi.GET.mockImplementation(
      (path: string, opts?: { params?: { path?: { componentRunId?: string } } }) => {
        if (path === "/api/applications/{id}/runs/{runId}")
          return Promise.resolve({ data: runDetail, error: undefined });
        const id = opts?.params?.path?.componentRunId;
        return Promise.resolve({
          data: { id, name: id, type: "helm", status: "succeeded", diff: `diff of ${id}`, has_changes: true },
          error: undefined,
        });
      },
    );
    const { unmount } = renderRunView();
    await clickStep("release");
    expect(await within(pane()).findByText("diff of cr-a")).toBeInTheDocument();
    expect(screen.getByTestId("location-search")).toHaveTextContent("?step=cr-a");
    unmount();

    // Arriving with ?step= opens that step rather than the failed one.
    renderRunView(undefined, "?step=cr-a");
    expect(await within(await screen.findByRole("region", { name: "Selected step" })).findByText("diff of cr-a")).toBeInTheDocument();
  });

  it("lists an OpenTofu component's plan and apply steps under it", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: awaitingDetail, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
    renderRunView();
    await screen.findByRole("navigation", { name: "Run steps" });
    const r = within(rail());
    expect(r.getByText("infra")).toBeInTheDocument();
    expect(r.getByRole("button", { name: /^plan/ })).toBeInTheDocument();
    expect(r.getByRole("button", { name: /^apply/ })).toBeInTheDocument();
    // Parked at the apply's gate: that's the step the view follows.
    expect(r.getByRole("button", { name: /^apply/ })).toHaveAttribute("aria-current", "true");
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
    await clickStep("release");
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

  it("expands the panel to fill the view, hiding the rail", async () => {
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
    await clickStep("release");
    await screen.findByRole("button", { name: /expand panel/i });
    // Expanding unmounts the rail, leaving the panel the whole content area;
    // collapsing brings it back.
    fireEvent.click(screen.getByRole("button", { name: /expand panel/i }));
    expect(screen.queryByRole("navigation", { name: "Run steps" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /collapse panel/i }));
    expect(await screen.findByRole("navigation", { name: "Run steps" })).toBeInTheDocument();
  });

  // breadcrumbLink finds a link in the page's breadcrumb trail.
  async function breadcrumbLink(name: string) {
    const trail = await screen.findByRole("navigation", { name: "Breadcrumb" });
    return within(trail).findByRole("link", { name });
  }

  function mockRunAndApp() {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: runDetail, error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: { id: "app-1", name: "web" }, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
  }

  it("leads back up to the application from the breadcrumb", async () => {
    mockRunAndApp();
    renderRunView();
    fireEvent.click(await breadcrumbLink("web"));
    expect(await screen.findByText("application page")).toBeInTheDocument();
  });

  it("leads to the application's run history by default", async () => {
    mockRunAndApp();
    renderRunView();
    fireEvent.click(await breadcrumbLink("Runs"));
    expect(await screen.findByText("runs index")).toBeInTheDocument();
    expect(screen.getByTestId("location-search")).toHaveTextContent(
      "?application=app-1",
    );
  });

  it("leads back to the runs index the user came from, filters and all", async () => {
    mockRunAndApp();
    renderRunView({ from: "/runs?status=failed" });
    fireEvent.click(await breadcrumbLink("Runs"));
    expect(await screen.findByText("runs index")).toBeInTheDocument();
    expect(screen.getByTestId("location-search")).toHaveTextContent(
      "?status=failed",
    );
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
    await clickStep("apply");
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
    await clickStep("apply");
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

  // A parsed plan as the API returns it for a settled OpenTofu plan step (the
  // list shape's plan summary plus, on the detail, per-resource diffs and the
  // plan body for an editor).
  type GetImpl = (
    path: string,
    opts?: unknown,
  ) => Promise<{ data?: unknown; error?: unknown }>;
  const parsedPlan = {
    has_changes: true,
    add: 1,
    change: 0,
    destroy: 1,
    replace: 1,
    outputs_changed: false,
    resources: [
      {
        address: "aws_db_instance.main",
        action: "replace",
        detail: "must be replaced",
        diff: "  # aws_db_instance.main must be replaced\n      ~ engine_version = \"14\" -> \"15\" # forces replacement",
      },
    ],
  };

  it("shows the structured plan on a parked tofu apply step when the plan step parsed", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockAwaitingComponentDetails();
    // Layer the parsed plan onto the plan step's detail.
    const base = mockApi.GET.getMockImplementation() as GetImpl;
    mockApi.GET.mockImplementation(async (path: string, opts?: unknown) => {
      const res = await base(path, opts);
      const o = opts as { params?: { path?: { componentRunId?: string } } } | undefined;
      if (
        path === "/api/applications/{id}/runs/{runId}/components/{componentRunId}" &&
        o?.params?.path?.componentRunId === "cr-a"
      ) {
        return { data: { ...(res.data as object), plan: parsedPlan, diff: "tofu plan body text", has_changes: true }, error: undefined };
      }
      return res;
    });
    renderRunView();
    await clickStep("apply");
    // The approver sees the totals, the destructive callout, and the resource
    // row — not a wall of logs.
    expect(
      await screen.findByText("Plan: 1 to add, 0 to change, 1 to destroy."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("1 resource will be destroyed and recreated."),
    ).toBeInTheDocument();
    expect(screen.getByText("aws_db_instance.main")).toBeInTheDocument();
    // The full plan text is a click away.
    expect(screen.queryByText("tofu plan body text")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /show full plan text/i }));
    expect(screen.getByText("tofu plan body text")).toBeInTheDocument();
  });

  it("leads with a settled tofu plan step's own parsed plan and shows counts on its row", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockAwaitingComponentDetails();
    // The run list carries the plan summary on the plan step's row (the node
    // badge reads it); the detail carries the structure.
    const withPlan = {
      ...awaitingDetail,
      component_runs: awaitingDetail.component_runs.map((cr) =>
        cr.id === "cr-a" ? { ...cr, plan: parsedPlan } : cr,
      ),
    };
    const base = mockApi.GET.getMockImplementation() as GetImpl;
    mockApi.GET.mockImplementation(async (path: string, opts?: unknown) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return { data: withPlan, error: undefined };
      const res = await base(path, opts);
      const o = opts as { params?: { path?: { componentRunId?: string } } } | undefined;
      if (o?.params?.path?.componentRunId === "cr-a")
        return { data: { ...(res.data as object), plan: parsedPlan }, error: undefined };
      return res;
    });
    renderRunView();
    // The plan step's rail row shows +1 ~0 -1 (and ±1 replaced).
    await screen.findByRole("navigation", { name: "Run steps" });
    expect(await within(rail()).findByText("+1")).toBeInTheDocument();
    expect(within(rail()).getByText("±1")).toBeInTheDocument();
    await clickStep("plan");
    // The Plan tab leads, with the summary.
    expect(
      await within(pane()).findByText("Plan: 1 to add, 0 to change, 1 to destroy."),
    ).toBeInTheDocument();
    expect(within(pane()).getByRole("button", { name: /^plan/i })).toBeInTheDocument();
  });

  it("follows a running step's output live, then hands over to the captured logs", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    // A deploy run with one step in flight that the worker has submitted (it
    // has a run name), so its pod output can be followed.
    const liveRun = {
      ...runningDetail,
      action: "deploy",
      component_runs: [
        { id: "cr-a", component_id: compA, name: "release", type: "helm", status: "succeeded" },
        { id: "cr-b", component_id: compB, name: "apply", type: "manifest", status: "running", run_name: "manifest-apply-x1" },
      ],
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: liveRun, error: undefined });
      return Promise.resolve({
        data: { id: "cr-b", name: "apply", type: "manifest", status: "running", run_name: "manifest-apply-x1", logs: "" },
        error: undefined,
      });
    });
    mockPodLogs.mockImplementation((_path: string, enabled: boolean) =>
      enabled
        ? { lines: ["kubectl apply -f .", "deployment.apps/web configured"], status: "live", ended: false, error: null }
        : { lines: [], status: "idle", ended: false, error: null },
    );
    renderRunView();
    await clickStep("apply");
    const logs = await screen.findByTestId("live-logs");
    expect(logs).toHaveTextContent("deployment.apps/web configured");
    // The stream was opened on the step's own log-stream path, enabled.
    expect(mockPodLogs).toHaveBeenCalledWith(
      "/api/applications/app-1/runs/run-1/components/cr-b/logs/stream",
      true,
    );
  });

  it("does not open a live log stream for a step that has no run name yet", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: { ...runningDetail, action: "deploy" }, error: undefined });
      return Promise.resolve({
        data: { id: "cr-b", name: "apply", type: "manifest", status: "running", logs: "" },
        error: undefined,
      });
    });
    renderRunView();
    await clickStep("apply");
    expect(
      await screen.findByText("No logs were captured for this step."),
    ).toBeInTheDocument();
    expect(mockPodLogs).not.toHaveBeenCalledWith(expect.any(String), true);
  });

  it("shows a drift check's finding on a Drift tab", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const driftRun = {
      ...runDetail,
      action: "drift",
      status: "succeeded",
      graph: JSON.stringify({
        nodes: [{ id: compA, name: "infra · plan", type: "terraform", config: { command: "plan" }, depends_on: [] }],
      }),
      component_runs: [
        { id: "cr-a", component_id: compA, name: "infra · plan", type: "terraform", status: "succeeded" },
      ],
      stages: tofuStages("succeeded"),
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: driftRun, error: undefined });
      return Promise.resolve({
        data: {
          id: "cr-a",
          name: "infra · plan",
          type: "terraform",
          status: "succeeded",
          logs: "drift logs",
          diff: "drift body",
          has_changes: true,
          plan: {
            has_changes: true, add: 0, change: 0, destroy: 0, replace: 0, resources: [],
            refresh_only: true, has_drift: true,
            drift: [{ address: "aws_instance.web", action: "drift_update", detail: "has been changed" }],
          },
        },
        error: undefined,
      });
    });
    renderRunView();
    await clickStep("infra");
    expect(await screen.findByRole("button", { name: /^drift/i })).toBeInTheDocument();
    expect(screen.getByText(/Drift detected: 1 resource changed outside of OpenTofu/)).toBeInTheDocument();
    expect(screen.getByText("aws_instance.web")).toBeInTheDocument();
    expect(screen.getByText("changed")).toBeInTheDocument();
  });

  it("shows a state operation's exact command at its approval gate", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const stateOp = {
      operation: "rm",
      address: "aws_instance.web",
      command: "tofu state rm aws_instance.web",
    };
    const opRun = {
      ...runDetail,
      action: "state_op",
      status: "awaiting_approval",
      finished_at: undefined,
      state_op: stateOp,
      graph: JSON.stringify({
        nodes: [{ id: compA, name: "infra · state rm", type: "terraform", config: { command: "state_op" }, depends_on: [], requires_approval: true }],
      }),
      component_runs: [
        { id: "cr-a", component_id: compA, name: "infra · state rm", type: "terraform", status: "awaiting_approval", requires_approval: true },
      ],
      stages: tofuStages("awaiting_approval"),
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: opRun, error: undefined });
      return Promise.resolve({
        data: { id: "cr-a", name: "infra · state rm", type: "terraform", status: "awaiting_approval" },
        error: undefined,
      });
    });
    renderRunView();
    expect(await screen.findByRole("heading", { name: "State operation" })).toBeInTheDocument();
    expect(screen.getByText(/Stop managing aws_instance.web/)).toBeInTheDocument();
    await clickStep("infra");
    expect(await screen.findByText("Awaiting approval")).toBeInTheDocument();
    expect(screen.getByTestId("state-op-command")).toHaveTextContent("tofu state rm aws_instance.web");
    expect(screen.getByText(/Approving runs this command/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /approve/i })).toBeInTheDocument();
  });

  it("labels a component-scoped run and says what it covers", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const scopedRun = {
      ...runDetail,
      action: "uninstall",
      scope: {
        component_id: compA,
        component_name: "infra",
        targets: ["aws_instance.web"],
        git_ref: "0123456789abcdef0123456789abcdef01234567",
      },
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: scopedRun, error: undefined });
      return Promise.resolve({ data: { id: "cr-a", name: "a", type: "helm", status: "succeeded" }, error: undefined });
    });
    renderRunView();
    expect(await screen.findByRole("heading", { name: "Component destroy" })).toBeInTheDocument();
    expect(screen.getByText("Only infra at 0123456, targeting aws_instance.web")).toBeInTheDocument();
  });

  it("says what triggered a run started from GitHub", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const triggered = {
      ...runDetail,
      action: "preview",
      trigger: { source: "github", event: "pull_request", repo: "acme/infra", branch: "feature/x", sha: "def4567890", sender: "kyle", pr_number: 12 },
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: triggered, error: undefined });
      return Promise.resolve({ data: { id: "cr-a", name: "a", type: "helm", status: "succeeded" }, error: undefined });
    });
    renderRunView();
    expect(
      await screen.findByText("Triggered by pull request #12 on acme/infra (feature/x @ def4567) by kyle"),
    ).toBeInTheDocument();
  });

  it("shows the policy verdict recorded on a plan step", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const verdict = {
      evaluated_at: "2026-09-08T00:00:00Z",
      blocked: true,
      warned: false,
      results: [
        { policy_id: "p1", policy_name: "no-db-deletes", enforcement: "block", violations: ["aws_db_instance.main would be destroyed"] },
        { policy_id: "p2", policy_name: "cap", enforcement: "warn", violations: [] },
      ],
    };
    const blockedRun = {
      ...runDetail,
      status: "failed",
      graph: JSON.stringify({
        nodes: [{ id: compA, name: "infra · plan", type: "terraform", config: { command: "plan" }, depends_on: [], requires_approval: false }],
      }),
      component_runs: [
        { id: "cr-a", component_id: compA, name: "infra · plan", type: "terraform", status: "failed", policy: verdict },
      ],
      stages: tofuStages("failed"),
    };
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: blockedRun, error: undefined });
      return Promise.resolve({
        data: { id: "cr-a", name: "infra · plan", type: "terraform", status: "failed", policy: verdict },
        error: undefined,
      });
    });
    renderRunView();
    await clickStep("infra");
    const box = await screen.findByTestId("policy-verdict");
    expect(box).toHaveTextContent("Blocked by policy");
    expect(box).toHaveTextContent("no-db-deletes (block) — aws_db_instance.main would be destroyed");
    expect(box).toHaveTextContent("cap (warn) — passed");
  });

  it("explains a gate's approval policy and tally", async () => {
    mockStream.mockReturnValue({ value: null, status: "live", error: null });
    const policyRun = {
      ...awaitingDetail,
      started_by: "dev@example.com",
      graph: JSON.stringify({
        nodes: [
          { id: compA, name: "infra · plan", type: "terraform", config: { command: "plan" }, depends_on: [] },
          {
            id: compB, name: "infra · apply", type: "terraform", config: { command: "apply" }, depends_on: [compA],
            approval_policy: { approvers: ["ops@example.com", "sre@example.com"], required: 2, require_different_approver: true, timeout_minutes: 60 },
          },
        ],
      }),
    };
    mockApi.GET.mockImplementation((path: string, opts?: { params?: { path?: { componentRunId?: string } } }) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: policyRun, error: undefined });
      const id = opts?.params?.path?.componentRunId;
      return Promise.resolve({
        data: id === "cr-b"
          ? { id: "cr-b", name: "infra · apply", type: "terraform", status: "awaiting_approval", approvals: [{ by: "ops@example.com", at: "2026-09-04T10:00:00Z" }] }
          : { id: "cr-a", name: "infra · plan", type: "terraform", status: "succeeded", logs: "plan" },
        error: undefined,
      });
    });
    renderRunView();
    expect(await screen.findByText(/by dev@example.com/)).toBeInTheDocument();
    await clickStep("apply");
    const line = await screen.findByTestId("approval-policy");
    expect(line).toHaveTextContent("Approvers: ops@example.com, sre@example.com");
    expect(line).toHaveTextContent("1 of 2 approvals (ops@example.com)");
    expect(line).toHaveTextContent("dev@example.com started this run and cannot approve it");
    expect(line).toHaveTextContent("times out after 60 min");
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
      stages: [
        {
          name: "Infrastructure",
          status: "succeeded",
          components: [
            { component_id: compA, name: "infra", type: "terraform", status: "succeeded", component_run_ids: ["cr-a", "cr-b"] },
          ],
        },
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
    await clickStep("apply");
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
    await clickStep("apply");
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
    await clickStep("apply");
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
    // The humanized label appears on the header badge.
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
    await clickStep("apply");
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

describe("delete after uninstall", () => {
  const uninstallDetail = { ...runDetail, action: "uninstall", status: "succeeded" };

  function mockUninstall(
    detail: Record<string, unknown>,
    latestRunId: string,
  ) {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/applications/{id}/runs/{runId}")
        return Promise.resolve({ data: detail, error: undefined });
      if (path === "/api/applications/{id}/runs")
        return Promise.resolve({ data: { runs: [{ id: latestRunId }] }, error: undefined });
      if (path === "/api/applications/{id}")
        return Promise.resolve({ data: { id: "app-1", name: "web" }, error: undefined });
      return Promise.resolve({ data: undefined, error: undefined });
    });
  }

  it("offers the delete once the uninstall succeeded, without asking about uninstalling again", async () => {
    mockUninstall(uninstallDetail, "run-1");
    mockApi.DELETE.mockResolvedValue({ data: undefined, error: undefined });
    renderRunView();

    fireEvent.click(await screen.findByRole("button", { name: "Delete application" }));
    const dialog = screen.getByRole("dialog", { name: /delete web/i });
    expect(within(dialog).queryByRole("radio")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("applications list")).toBeInTheDocument();
    expect(mockApi.DELETE).toHaveBeenCalledWith(
      "/api/applications/{id}",
      expect.objectContaining({ params: { path: { id: "app-1" } } }),
    );
  });

  it("makes no offer once a later run has superseded the uninstall", async () => {
    mockUninstall(uninstallDetail, "run-2");
    renderRunView();
    await screen.findByRole("navigation", { name: "Run steps" });
    await waitFor(() =>
      expect(mockApi.GET).toHaveBeenCalledWith("/api/applications/{id}/runs", expect.anything()),
    );
    expect(screen.queryByRole("button", { name: "Delete application" })).toBeNull();
  });

  describe("a component's own uninstall", () => {
    const componentUninstall = {
      ...uninstallDetail,
      scope: { component_id: compA, component_name: "release", component_type: "helm" },
    };

    function mockComponentUninstall(stillThere: boolean) {
      mockApi.GET.mockImplementation((path: string) => {
        if (path === "/api/applications/{id}/runs/{runId}")
          return Promise.resolve({ data: componentUninstall, error: undefined });
        if (path === "/api/applications/{id}/runs")
          return Promise.resolve({ data: { runs: [{ id: "run-1" }] }, error: undefined });
        if (path === "/api/applications/{id}/workflow")
          return Promise.resolve({
            data: {
              stages: [
                {
                  id: "st-1",
                  name: "Apps",
                  components: stillThere ? [{ id: compA, name: "release" }] : [],
                },
              ],
            },
            error: undefined,
          });
        if (path === "/api/applications/{id}")
          return Promise.resolve({ data: { id: "app-1", name: "web" }, error: undefined });
        return Promise.resolve({ data: undefined, error: undefined });
      });
    }

    function ComponentPage() {
      const state = useLocation().state as { deleteAfterUninstall?: boolean } | null;
      return <p>component page{state?.deleteAfterUninstall ? ", deleting" : ""}</p>;
    }

    it("offers the component's delete, on its page with the question answered", async () => {
      mockComponentUninstall(true);
      render(
        <MemoryRouter initialEntries={["/applications/app-1/runs/run-1"]}>
          <Routes>
            <Route path="/applications/:appId/runs/:runId" element={<WorkflowRunView />} />
            <Route path="/applications/:appId/workflow/nodes/:nodeId" element={<ComponentPage />} />
          </Routes>
        </MemoryRouter>,
      );
      expect(await screen.findByText(/has been uninstalled/)).toBeInTheDocument();
      expect(screen.getAllByText("Component uninstall").length).toBeGreaterThan(0);
      expect(screen.queryByRole("button", { name: "Delete application" })).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Delete component" }));
      expect(await screen.findByText("component page, deleting")).toBeInTheDocument();
    });

    it("makes no offer once the component is gone", async () => {
      mockComponentUninstall(false);
      renderRunView();
      await screen.findByRole("navigation", { name: "Run steps" });
      await waitFor(() =>
        expect(mockApi.GET).toHaveBeenCalledWith("/api/applications/{id}/workflow", expect.anything()),
      );
      expect(screen.queryByRole("button", { name: "Delete component" })).toBeNull();
    });
  });

  it("makes no offer for a single component's destroy", async () => {
    mockUninstall(
      { ...uninstallDetail, scope: { component_id: compA, component_name: "release" } },
      "run-1",
    );
    renderRunView();
    await screen.findByRole("navigation", { name: "Run steps" });
    expect(screen.queryByRole("button", { name: "Delete application" })).toBeNull();
  });
});
