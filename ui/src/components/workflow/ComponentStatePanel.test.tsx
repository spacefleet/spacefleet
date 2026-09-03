import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { ComponentStatePanel } from "./ComponentStatePanel";

vi.mock("../../api/client", () => ({
  api: { GET: vi.fn() },
}));
const mockGet = api.GET as unknown as ReturnType<typeof vi.fn>;

function renderPanel() {
  return render(
    <MemoryRouter>
      <ComponentStatePanel appId="app-1" componentId="comp-1" />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockGet.mockReset();
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
});
