import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Home } from "./Home";
import { api } from "../api/client";

vi.mock("../api/client", () => ({ api: { GET: vi.fn() } }));
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: "editor" }),
}));
const mockGet = api.GET as unknown as ReturnType<typeof vi.fn>;

const runs = [
  { id: "r1", application_id: "a1", action: "deploy", status: "awaiting_approval", created_at: "2026-09-08T10:00:00Z" },
  { id: "r2", application_id: "a2", action: "preview", status: "failed", message: "workflow failed", created_at: "2026-09-08T09:00:00Z" },
  { id: "r3", application_id: "a1", action: "drift", status: "succeeded", created_at: "2026-09-08T08:00:00Z" },
];

function mockAll(withRuns = runs) {
  mockGet.mockImplementation((path: string) => {
    if (path === "/api/applications")
      return Promise.resolve({ data: [{ id: "a1", name: "web" }, { id: "a2", name: "infra" }], error: undefined });
    if (path === "/api/clusters")
      return Promise.resolve({ data: [{ id: "c1", name: "prod" }], error: undefined });
    if (path === "/api/runs") return Promise.resolve({ data: { runs: withRuns }, error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
}

function renderHome() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/applications/:appId/runs/:runId" element={<div>run page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => mockGet.mockReset());

describe("Home", () => {
  it("shows counts, what needs attention, and recent runs", async () => {
    mockAll();
    renderHome();
    expect(await screen.findByText("Acme")).toBeInTheDocument();
    expect((await screen.findByText("Applications")).closest("a")).toHaveTextContent("2");
    expect(screen.getByText("Clusters").closest("a")).toHaveTextContent("1");
    expect(screen.getByText("Runs in flight").closest("a")).toHaveTextContent("1");
    expect(screen.getByText("Needs attention")).toBeInTheDocument();
    expect(screen.getByText("waiting for approval")).toBeInTheDocument();
    expect(screen.getByText("workflow failed")).toBeInTheDocument();
    // The recent list carries every run; the drift check appears once (it
    // needs no attention).
    expect(screen.getAllByText("drift")).toHaveLength(1);
    fireEvent.click(screen.getByText("drift"));
    expect(await screen.findByText("run page")).toBeInTheDocument();
  });

  it("points a fresh organization at creating an application", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/api/runs") return Promise.resolve({ data: { runs: [] }, error: undefined });
      return Promise.resolve({ data: [], error: undefined });
    });
    renderHome();
    expect(await screen.findByText(/No runs yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an application" })).toHaveAttribute("href", "/applications");
    expect(screen.queryByText("Needs attention")).not.toBeInTheDocument();
  });
});
