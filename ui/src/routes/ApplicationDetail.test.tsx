import { render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationDetail } from "./ApplicationDetail";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn(), PATCH: vi.fn() },
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
  PATCH: ReturnType<typeof vi.fn>;
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

const RELEASE = "11111111-1111-1111-1111-111111111111";
const APPLY = "22222222-2222-2222-2222-222222222222";

// The latest run: release succeeded, then apply failed. Its stage summary is
// what colors the overview and fills the stage bar.
const run = {
  id: "run-2",
  application_id: "app-1",
  action: "deploy",
  status: "succeeded",
  created_at: "2026-06-03T10:00:00Z",
  finished_at: "2026-06-03T10:01:00Z",
  stages: [
    {
      name: "Charts",
      status: "succeeded",
      components: [
        { component_id: RELEASE, name: "release", type: "helm", status: "succeeded", component_run_ids: ["cr-1"] },
      ],
    },
    {
      name: "Manifests",
      status: "failed",
      components: [
        { component_id: APPLY, name: "apply", type: "manifest", status: "failed", component_run_ids: ["cr-2"] },
      ],
    },
  ],
};

// A two-stage workflow for the at-a-glance overview card.
const workflow = {
  stages: [
    {
      id: "s1",
      name: "Charts",
      components: [
        {
          id: RELEASE,
          name: "release",
          type: "helm",
          config: {},
          continue_on_failure: false,
          target_cluster_id: "c1",
          target_namespace: "web",
        },
      ],
    },
    {
      id: "s2",
      name: "Manifests",
      components: [
        {
          id: APPLY,
          name: "apply",
          type: "manifest",
          config: { path: "k8s" },
          continue_on_failure: false,
          target_namespace: "",
        },
      ],
    },
  ],
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1"]}>
      <Routes>
        <Route path="/applications/:appId" element={<ApplicationDetail />} />
        <Route path="/applications" element={<div>applications list</div>} />
        <Route
          path="/applications/:appId/edit"
          element={<div>edit application</div>}
        />
        <Route
          path="/applications/:appId/variables"
          element={<div>variables page</div>}
        />
        <Route
          path="/applications/:appId/workflow"
          element={<div>workflow builder</div>}
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
  mockApi.PATCH.mockReset();
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
    expect(await screen.findByText("workflow builder")).toBeInTheDocument();
  });

  it("links the full run history from the latest run, not the workflow card", async () => {
    renderDetail();
    await screen.findByText("web");
    expect(screen.queryByRole("button", { name: /run history/i })).toBeNull();
    expect(screen.getByRole("link", { name: "View all runs" })).toHaveAttribute(
      "href",
      "/runs?application=app-1",
    );
  });

  it("leaves trigger settings to the edit form", async () => {
    renderDetail();
    await screen.findByText("web");
    expect(screen.queryByLabelText("On push")).toBeNull();
    expect(screen.queryByLabelText("Plan pull requests")).toBeNull();
  });

  it("opens Manage and Variables from the header, with Delete behind the actions menu", async () => {
    renderDetail();
    const menu = await screen.findByRole("button", { name: "web actions" });
    // Variables live on their own page now, not on this one.
    expect(screen.queryByText("No variables.")).toBeNull();

    await userEvent.click(menu);
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "Delete",
    ]);
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(
      screen.getByRole("heading", { name: /delete web/i }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /cancel/i }));

    await userEvent.click(screen.getByRole("button", { name: "Variables" }));
    expect(await screen.findByText("variables page")).toBeInTheDocument();
  });

  it("opens the application's settings from Manage", async () => {
    renderDetail();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    expect(await screen.findByText("edit application")).toBeInTheDocument();
  });

  it("shows the workflow's stages and components, colored by the latest run", async () => {
    renderDetail();
    const charts = await screen.findByRole("region", { name: "Stage Charts" });
    expect(within(charts).getByText("release")).toBeInTheDocument();
    // The helm card's summary is its deploy target.
    expect(within(charts).getByText("prod / web")).toBeInTheDocument();
    const manifests = screen.getByRole("region", { name: "Stage Manifests" });
    const applyCard = within(manifests).getByRole("button", { name: /apply/ });
    // apply failed in the latest run: its card takes the failed colors.
    expect(applyCard.className).toContain("border-red-500");
  });

  it("shows the latest run's stages as a bar", async () => {
    renderDetail();
    expect(
      await screen.findByRole("img", {
        name: "Stages — Charts: succeeded, Manifests: failed",
      }),
    ).toBeInTheDocument();
  });

  it("opens the builder from a component in the overview", async () => {
    renderDetail();
    const charts = await screen.findByRole("region", { name: "Stage Charts" });
    await userEvent.click(within(charts).getByRole("button", { name: /release/ }));
    expect(await screen.findByText("workflow builder")).toBeInTheDocument();
  });

  it("opens the Run dialog rather than deploying right away, then deploys from it", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await screen.findByText("web");
    // The run options live in the dialog, not on the page.
    expect(screen.queryByRole("checkbox", { name: /force workload roll/i })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /^run$/i }));
    expect(mockApi.POST).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "Run web" });
    await userEvent.click(within(dialog).getByRole("button", { name: /^run$/i }));

    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: false } }),
    );
  });

  it("sends force=true when Force workload roll is checked in the Run dialog", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^run$/i }));
    const dialog = screen.getByRole("dialog", { name: "Run web" });
    await userEvent.click(
      within(dialog).getByRole("checkbox", { name: /force workload roll/i }),
    );
    await userEvent.click(within(dialog).getByRole("button", { name: /^run$/i }));
    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "deploy", force: true } }),
    );
  });

  it("closes the Run dialog on Cancel without starting anything", async () => {
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^run$/i }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("shows an in-progress message in the Run dialog on a 409", async () => {
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^run$/i }));
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: /^run$/i }));
    expect(
      await within(dialog).findByText(/a run is already in progress/i),
    ).toBeInTheDocument();
  });

  it("shows an in-progress message on the page when a preview hits a 409", async () => {
    mockApi.POST.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^preview$/i }));
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

describe("run row", () => {
  it("leaves the refresh schedule to Manage", async () => {
    renderDetail();
    await screen.findByText("web");
    expect(screen.queryByLabelText(/refresh schedule|scheduled refresh/i)).toBeNull();
  });

  it("has no Uninstall in the run row — it lives in the Delete dialog", async () => {
    renderDetail();
    await screen.findByText("web");
    expect(screen.queryByRole("button", { name: /uninstall/i })).toBeNull();
    expect(screen.queryByRole("button", { name: "More run actions" })).toBeNull();
  });

  it("starts an uninstall from the Delete dialog and opens its run instead of deleting", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await userEvent.click(await screen.findByRole("button", { name: "web actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    const dialog = screen.getByRole("dialog", { name: /delete web/i });
    // Nothing is preselected: both choices are destructive in their own way.
    const confirm = within(dialog).getByRole("button", { name: "Delete" });
    expect(confirm).toBeDisabled();

    await userEvent.click(within(dialog).getByRole("radio", { name: /uninstall it first/i }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Start uninstall" }));

    expect(await screen.findByText("run view")).toBeInTheDocument();
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "uninstall" } }),
    );
    expect(mockApi.DELETE).not.toHaveBeenCalled();
  });

  it("deletes right away when leaving everything running", async () => {
    mockApi.DELETE.mockResolvedValue({ data: undefined, error: undefined });
    renderDetail();
    await userEvent.click(await screen.findByRole("button", { name: "web actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    const dialog = screen.getByRole("dialog", { name: /delete web/i });
    await userEvent.click(within(dialog).getByRole("radio", { name: /leave it running/i }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("applications list")).toBeInTheDocument();
    expect(mockApi.DELETE).toHaveBeenCalledWith(
      "/api/applications/{id}",
      expect.objectContaining({ params: { path: { id: "app-1" } } }),
    );
    expect(mockApi.POST).not.toHaveBeenCalled();
  });

  it("starts a drift check from the Refresh button", async () => {
    mockApi.POST.mockResolvedValue({
      data: { id: "run-9" },
      error: undefined,
      response: { status: 202 },
    });
    renderDetail();
    await screen.findByText("web");
    await userEvent.click(screen.getByRole("button", { name: /^refresh$/i }));
    expect(mockApi.POST).toHaveBeenCalledWith(
      "/api/applications/{id}/runs",
      expect.objectContaining({ body: { action: "drift" } }),
    );
  });
});
