import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { ComponentStatePanel } from "./ComponentStatePanel";

vi.mock("../../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}));
const mockGet = api.GET as unknown as ReturnType<typeof vi.fn>;
const mockPost = api.POST as unknown as ReturnType<typeof vi.fn>;

function renderPanel(canEdit = false, managedBackend = false) {
  return render(
    <MemoryRouter initialEntries={["/node"]}>
      <Routes>
        <Route
          path="/node"
          element={
            <ComponentStatePanel
              appId="app-1"
              componentId="comp-1"
              canEdit={canEdit}
              managedBackend={managedBackend}
            />
          }
        />
        <Route path="/applications/:appId/runs/:runId" element={<div>run page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockGet.mockReset();
  mockPost.mockReset();
});

describe("ComponentStatePanel", () => {
  it("shows the recorded resources, switches to outputs, and links to the run", async () => {
    mockGet.mockResolvedValue({
      data: {
        run_id: "run-9",
        component_run_id: "cr-9",
        recorded_at: "2026-09-03T10:00:00Z",
        outputs: { vpc_id: { value: "vpc-1", sensitive: false } },
        resources: [
          { address: "aws_vpc.main", mode: "managed", type: "aws_vpc", name: "main", provider: "hashicorp/aws", id: "vpc-1" },
        ],
      },
      error: undefined,
      response: { status: 200 },
    });
    renderPanel();
    expect(await screen.findByText("aws_vpc.main")).toBeInTheDocument();
    expect(mockGet).toHaveBeenCalledWith(
      "/api/applications/{id}/components/{componentId}/state",
      { params: { path: { id: "app-1", componentId: "comp-1" } } },
    );
    expect(screen.getByRole("link", { name: "this run" })).toHaveAttribute(
      "href",
      "/applications/app-1/runs/run-9",
    );
    fireEvent.click(screen.getByRole("button", { name: /outputs/i }));
    expect(screen.getByText("vpc_id")).toBeInTheDocument();
    expect(screen.getByText("vpc-1")).toBeInTheDocument();
  });

  it("shows a quiet placeholder for a component that has never applied", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    renderPanel();
    expect(
      await screen.findByText(/Nothing recorded yet/),
    ).toBeInTheDocument();
  });

  it("surfaces other failures as an error", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "boom" },
      response: { status: 500 },
    });
    renderPanel();
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });

  it("hides the state operations from a viewer", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    renderPanel(false);
    await screen.findByText(/Nothing recorded yet/);
    expect(screen.queryByText("Operations")).not.toBeInTheDocument();
  });

  it("starts a guarded state operation and goes to its run", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    mockPost.mockResolvedValue({
      data: { id: "run-42", action: "state_op", status: "pending" },
      error: undefined,
    });
    renderPanel(true);
    await screen.findByText("Operations");
    const start = screen.getByRole("button", { name: "Start for approval" });
    expect(start).toBeDisabled();

    // Switch to a move: its two fields appear, the button stays disabled
    // until both are filled.
    fireEvent.change(screen.getByLabelText("State operation"), { target: { value: "mv" } });
    fireEvent.change(screen.getByLabelText("Current address"), { target: { value: "aws_instance.web" } });
    expect(start).toBeDisabled();
    fireEvent.change(screen.getByLabelText("New address"), { target: { value: " module.web.aws_instance.this " } });
    expect(start).toBeEnabled();
    fireEvent.click(start);

    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/state-ops",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: { operation: "mv", address: "aws_instance.web", new_address: "module.web.aws_instance.this" },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("shows a stuck lock and prefills the force-unlock operation for an editor", async () => {
    mockGet.mockResolvedValue({
      data: {
        run_id: "run-9",
        component_run_id: "cr-9",
        resources: [],
        lock: {
          run_id: "run-10",
          component_run_id: "cr-10",
          failed_at: "2026-09-03T11:00:00Z",
          id: "6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f",
          who: "root@pod",
          created: "2026-09-03 10:59:00 +0000 UTC",
          operation: "OperationTypeApply",
        },
      },
      error: undefined,
      response: { status: 200 },
    });
    mockPost.mockResolvedValue({
      data: { id: "run-42", action: "state_op", status: "pending" },
      error: undefined,
    });
    renderPanel(true);
    expect(await screen.findByText("State is locked.")).toBeInTheDocument();
    expect(screen.getByText("root@pod")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /The run on/ })).toHaveAttribute(
      "href",
      "/applications/app-1/runs/run-10",
    );

    fireEvent.click(screen.getByRole("button", { name: "Release this lock…" }));
    expect(screen.getByLabelText("State operation")).toHaveValue("force_unlock");
    expect(screen.getByLabelText("Lock id")).toHaveValue("6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f");
    const start = screen.getByRole("button", { name: "Start for approval" });
    expect(start).toBeEnabled();
    fireEvent.click(start);
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/state-ops",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: { operation: "force_unlock", lock_id: "6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f" },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("shows a stuck lock before the first apply, with nothing recorded", async () => {
    mockGet.mockResolvedValue({
      data: { resources: [], lock: { run_id: "run-10", component_run_id: "cr-10", id: "lock-1" } },
      error: undefined,
      response: { status: 200 },
    });
    renderPanel(true);
    expect(await screen.findByText("State is locked.")).toBeInTheDocument();
    expect(screen.getByText(/Nothing recorded yet/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "this run" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Release this lock…" })).toBeInTheDocument();
  });

  it("shows a stuck lock to a viewer without the release button", async () => {
    mockGet.mockResolvedValue({
      data: {
        run_id: "run-9",
        component_run_id: "cr-9",
        resources: [],
        lock: { run_id: "run-10", component_run_id: "cr-10", id: "lock-1" },
      },
      error: undefined,
      response: { status: 200 },
    });
    renderPanel(false);
    expect(await screen.findByText("State is locked.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release this lock…" })).not.toBeInTheDocument();
  });

  it("starts a per-component destroy only after confirming, and goes to its run", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    mockPost.mockResolvedValue({
      data: { id: "run-43", action: "uninstall", status: "pending" },
      error: undefined,
    });
    renderPanel(true);
    await screen.findByText("Destroy and targeted runs");
    fireEvent.click(screen.getByRole("button", { name: "Destroy this component…" }));
    expect(mockPost).not.toHaveBeenCalled();
    expect(
      screen.getByText(/destruction of every resource this component manages/),
    ).toBeInTheDocument();
    // Backing out hides the confirmation without starting anything.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText(/destruction of every resource/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Destroy this component…" }));
    fireEvent.click(screen.getByRole("button", { name: "Start destroy for approval" }));

    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/runs",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: { action: "uninstall" },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("starts a targeted deploy with the listed addresses", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    mockPost.mockResolvedValue({
      data: { id: "run-44", action: "deploy", status: "pending" },
      error: undefined,
    });
    renderPanel(true);
    await screen.findByText("Destroy and targeted runs");
    fireEvent.change(screen.getByLabelText("Target addresses"), {
      target: { value: " aws_instance.web\nmodule.vpc.aws_subnet.private[0], " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Deploy targets" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/api/applications/{id}/components/{componentId}/runs",
        {
          params: { path: { id: "app-1", componentId: "comp-1" } },
          body: {
            action: "deploy",
            targets: ["aws_instance.web", "module.vpc.aws_subnet.private[0]"],
          },
        },
      ),
    );
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("shows the API's reason when a scoped run is refused", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    mockPost.mockResolvedValue({
      data: undefined,
      error: { message: 'tofu: invalid target address: "aws_instance" is not a resource address' },
      response: { status: 400 },
    });
    renderPanel(true);
    await screen.findByText("Destroy and targeted runs");
    fireEvent.change(screen.getByLabelText("Target addresses"), { target: { value: "aws_instance" } });
    fireEvent.click(screen.getByRole("button", { name: "Deploy targets" }));
    expect(await screen.findByText(/is not a resource address/)).toBeInTheDocument();
  });

  it("shows the API's reason when an operation is refused", async () => {
    mockGet.mockResolvedValue({
      data: undefined,
      error: { message: "no recorded state for this component" },
      response: { status: 404 },
    });
    mockPost.mockResolvedValue({
      data: undefined,
      error: { message: "a run is already in progress for this application" },
      response: { status: 409 },
    });
    renderPanel(true);
    await screen.findByText("Operations");
    fireEvent.change(screen.getByLabelText("Resource address"), { target: { value: "aws_instance.web" } });
    fireEvent.click(screen.getByRole("button", { name: "Start for approval" }));
    expect(
      await screen.findByText("a run is already in progress for this application"),
    ).toBeInTheDocument();
  });

  describe("managed state", () => {
    const managedView = {
      data: {
        resources: [],
        managed_state: {
          version: 3,
          serial: 7,
          size_bytes: 512,
          written_at: "2026-10-01T10:00:00Z",
          run_id: "run-3",
        },
      },
      error: undefined,
      response: { status: 200 },
    };

    it("names the current version and lets an editor download it", async () => {
      mockGet.mockResolvedValueOnce(managedView).mockResolvedValueOnce({
        data: new Blob(["{}"]),
        error: undefined,
        response: {
          status: 200,
          headers: new Headers({
            "Content-Disposition": 'attachment; filename="web-infra.tfstate"',
          }),
        },
      });
      const createObjectURL = vi.fn(() => "blob:state");
      const revokeObjectURL = vi.fn();
      Object.assign(URL, { createObjectURL, revokeObjectURL });
      const click = vi
        .spyOn(HTMLAnchorElement.prototype, "click")
        .mockImplementation(() => {});

      renderPanel(true, true);
      expect(await screen.findByText(/version 3, written/)).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: /download state/i }));
      await waitFor(() => expect(click).toHaveBeenCalled());
      expect(mockGet).toHaveBeenLastCalledWith(
        "/api/applications/{id}/components/{componentId}/state/download",
        { params: { path: { id: "app-1", componentId: "comp-1" } }, parseAs: "blob" },
      );
      const anchor = click.mock.contexts[0] as HTMLAnchorElement;
      expect(anchor.download).toBe("web-infra.tfstate");
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:state");
      click.mockRestore();
    });

    it("shows the version without a download to a viewer", async () => {
      mockGet.mockResolvedValue(managedView);
      renderPanel(false, true);
      expect(await screen.findByText(/version 3, written/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /download state/i })).toBeNull();
    });

    it("hides it for a component on a cloud backend", async () => {
      mockGet.mockResolvedValue(managedView);
      renderPanel(true, false);
      expect(
        await screen.findByText(/Nothing recorded yet/),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Managed state/)).toBeNull();
    });
  });
});
