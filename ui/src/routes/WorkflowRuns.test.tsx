import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowRuns } from "./WorkflowRuns";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: "editor" }),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

const runs = [
  {
    id: "run-2",
    application_id: "app-1",
    action: "deploy",
    status: "succeeded",
    created_at: "2026-06-03T10:00:00Z",
    finished_at: "2026-06-03T10:02:00Z",
  },
  {
    id: "run-1",
    application_id: "app-1",
    action: "preview",
    status: "partial",
    created_at: "2026-06-03T09:00:00Z",
    finished_at: "2026-06-03T09:01:00Z",
  },
];

function renderRuns() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/runs"]}>
      <Routes>
        <Route path="/applications/:appId/runs" element={<WorkflowRuns />} />
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
});

describe("WorkflowRuns", () => {
  it("lists runs newest-first with status and action", async () => {
    mockApi.GET.mockResolvedValue({ data: { runs }, error: undefined });
    renderRuns();
    expect(await screen.findByText("deploy")).toBeInTheDocument();
    expect(screen.getByText("preview")).toBeInTheDocument();
    expect(screen.getByText("succeeded")).toBeInTheDocument();
    expect(screen.getByText("partial")).toBeInTheDocument();
  });

  it("shows the empty state when there are no runs", async () => {
    mockApi.GET.mockResolvedValue({ data: { runs: [] }, error: undefined });
    renderRuns();
    expect(await screen.findByText(/no runs yet/i)).toBeInTheDocument();
  });

  it("navigates to a run's view when clicked", async () => {
    mockApi.GET.mockResolvedValue({ data: { runs }, error: undefined });
    renderRuns();
    await userEvent.click(await screen.findByText("deploy"));
    expect(await screen.findByText("run view")).toBeInTheDocument();
  });

  it("renders an error when the list fails", async () => {
    mockApi.GET.mockResolvedValue({ data: undefined, error: { message: "boom" } });
    renderRuns();
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });
});
