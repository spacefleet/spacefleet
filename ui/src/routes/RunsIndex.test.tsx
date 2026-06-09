import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RunsIndex } from "./RunsIndex";
import { api } from "../api/client";
import { useObjectStream } from "../lib/useObjectStream";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: "editor" }),
}));

// The live stream is exercised on its own; here we keep it idle so the table
// reflects the initial fetch.
vi.mock("../lib/useObjectStream", () => ({
  useObjectStream: vi.fn(),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };
const mockStream = useObjectStream as unknown as ReturnType<typeof vi.fn>;

const apps = [
  {
    id: "app-1",
    name: "checkout",
    runner_cluster_id: "cl-ci",
    created_at: "",
    updated_at: "",
  },
  {
    id: "app-2",
    name: "billing",
    runner_cluster_id: "cl-stage",
    created_at: "",
    updated_at: "",
  },
];

const clusters = [
  { id: "cl-prod", name: "production" },
  { id: "cl-stage", name: "staging" },
  { id: "cl-ci", name: "ci-runner" },
];

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
    application_id: "app-2",
    action: "preview",
    status: "partial",
    created_at: "2026-06-03T09:00:00Z",
    finished_at: "2026-06-03T09:01:00Z",
  },
];

// Route each endpoint to its canned payload regardless of call order.
function routeGet() {
  mockApi.GET.mockImplementation((path: string) => {
    switch (path) {
      case "/api/applications":
        return Promise.resolve({ data: apps, error: undefined });
      case "/api/clusters":
        return Promise.resolve({ data: clusters, error: undefined });
      case "/api/runs":
        return Promise.resolve({ data: { runs }, error: undefined });
      default:
        return Promise.resolve({ data: undefined, error: { message: "unexpected" } });
    }
  });
}

function renderIndex(initial = "/runs") {
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <Routes>
        <Route path="/runs" element={<RunsIndex />} />
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
  mockStream.mockReset();
  mockStream.mockReturnValue({ value: null, status: "live", error: null });
});

// Names appear both in the filter dropdowns and the table, so assertions are
// scoped to the table body to avoid matching the <option> elements.
async function findTableBody() {
  const table = await screen.findByRole("table");
  return within(table).getAllByRole("row")[1].closest("tbody") as HTMLElement;
}

describe("RunsIndex", () => {
  it("renders runs with application and runner cluster name", async () => {
    routeGet();
    renderIndex();

    const body = await findTableBody();
    const checkout = within(body).getByText("checkout");
    const row = checkout.closest("tr") as HTMLElement;
    expect(within(row).getByText("ci-runner")).toBeInTheDocument(); // runner cluster
    expect(within(row).getByText("succeeded")).toBeInTheDocument();

    expect(within(body).getByText("billing")).toBeInTheDocument();
    // billing's runner resolves to its name (the deploy target is per-component).
    expect(within(body).getByText("staging")).toBeInTheDocument();
  });

  it("filters by application", async () => {
    routeGet();
    renderIndex();
    await findTableBody();

    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: /Application/i }),
      "billing",
    );

    const body = await findTableBody();
    expect(within(body).queryByText("checkout")).not.toBeInTheDocument();
    expect(within(body).getByText("billing")).toBeInTheDocument();
  });

  it("filters by runner cluster", async () => {
    routeGet();
    renderIndex();
    await findTableBody();

    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: /Runner cluster/i }),
      "ci-runner",
    );

    const body = await findTableBody();
    expect(within(body).getByText("checkout")).toBeInTheDocument();
    expect(within(body).queryByText("billing")).not.toBeInTheDocument();
  });

  it("seeds the application filter from the ?application= query param", async () => {
    routeGet();
    renderIndex("/runs?application=app-2");
    const body = await findTableBody();
    expect(within(body).getByText("billing")).toBeInTheDocument();
    expect(within(body).queryByText("checkout")).not.toBeInTheDocument();
  });

  it("replaces the table when a live snapshot arrives", async () => {
    routeGet();
    // The real hook only connects once loading finishes (enabled = !loading), so
    // its first snapshot lands after the initial fetch. Mirror that here so the
    // snapshot wins rather than racing the fetch.
    const snapshot = {
      runs: [
        {
          id: "run-3",
          application_id: "app-1",
          action: "uninstall",
          status: "running",
          created_at: "2026-06-03T11:00:00Z",
          finished_at: null,
        },
      ],
    };
    mockStream.mockImplementation((_path: string, enabled: boolean) => ({
      value: enabled ? snapshot : null,
      status: "live",
      error: null,
    }));
    renderIndex();

    await waitFor(() =>
      expect(screen.getByText("uninstall")).toBeInTheDocument(),
    );
    expect(screen.queryByText("deploy")).not.toBeInTheDocument();
  });
});
