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
const onReleaseLock = vi.fn();

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
              onReleaseLock={onReleaseLock}
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
  onReleaseLock.mockReset();
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

  it("shows a stuck lock and hands its id to the operations for an editor", async () => {
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
    renderPanel(true);
    expect(await screen.findByText("State is locked.")).toBeInTheDocument();
    expect(screen.getByText("root@pod")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /The run on/ })).toHaveAttribute(
      "href",
      "/applications/app-1/runs/run-10",
    );

    fireEvent.click(screen.getByRole("button", { name: "Release this lock…" }));
    expect(onReleaseLock).toHaveBeenCalledWith("6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f");
    // The operations are a card of their own now.
    expect(screen.queryByText("Operations")).not.toBeInTheDocument();
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
