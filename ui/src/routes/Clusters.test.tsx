import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Clusters } from "./Clusters";
import { api } from "../api/client";

// The API client and org context are mocked so we can drive the page's data
// without a backend.
vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" } }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  POST: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.POST.mockReset();
  mockApi.DELETE.mockReset();
});

describe("Clusters", () => {
  it("shows the empty state when there are no clusters", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    render(<Clusters />);
    expect(await screen.findByText("No clusters yet")).toBeInTheDocument();
  });

  it("renders registered clusters with their status", async () => {
    mockApi.GET.mockResolvedValue({
      data: [
        {
          id: "c1",
          name: "prod",
          connection_method: "eks",
          status: "connected",
          config: {},
          k8s_version: "v1.30.1",
          created_at: "2026-05-30T00:00:00Z",
          updated_at: "2026-05-30T00:00:00Z",
        },
      ],
      error: undefined,
    });
    render(<Clusters />);
    expect(await screen.findByText("prod")).toBeInTheDocument();
    expect(screen.getByText("Amazon EKS")).toBeInTheDocument();
    expect(screen.getByText("connected")).toBeInTheDocument();
    expect(screen.getByText("v1.30.1")).toBeInTheDocument();
  });

  it("opens the registration dialog with method options", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    render(<Clusters />);
    await screen.findByText("No clusters yet");

    await userEvent.click(screen.getByRole("button", { name: "Add cluster" }));

    expect(
      screen.getByRole("heading", { name: "Add cluster" }),
    ).toBeInTheDocument();
    // The in-cluster method is selected by default and needs no credentials.
    expect(
      screen.getByText(/Spacefleet is running in this cluster/),
    ).toBeInTheDocument();
  });
});
