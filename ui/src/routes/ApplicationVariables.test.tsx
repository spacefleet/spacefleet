import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApplicationVariables } from "./ApplicationVariables";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
}));

let role = "editor";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentOrg: { id: "org-1", name: "Acme" }, currentRole: role }),
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

const logLevel = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "LOG_LEVEL",
  sensitive: false,
  value: "debug",
  created_at: "2026-06-08T00:00:00Z",
  updated_at: "2026-06-08T00:00:00Z",
};

beforeEach(() => {
  role = "editor";
  mockApi.GET.mockReset();
  mockApi.GET.mockImplementation((path: string) => {
    if (path === "/api/applications/{id}")
      return Promise.resolve({ data: { id: "app-1", name: "web" }, error: undefined });
    if (path === "/api/applications/{id}/variables")
      return Promise.resolve({ data: [logLevel], error: undefined });
    return Promise.resolve({ data: undefined, error: undefined });
  });
});

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/variables"]}>
      <Routes>
        <Route
          path="/applications/:appId/variables"
          element={<ApplicationVariables />}
        />
        <Route path="/applications/:appId" element={<div>application page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ApplicationVariables", () => {
  it("lists the application's variables and lets an editor add one", async () => {
    renderPage();
    expect(await screen.findByText("LOG_LEVEL")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Variables" })).toBeInTheDocument();
    expect(await screen.findByText("web")).toBeInTheDocument();
    expect(mockApi.GET).toHaveBeenCalledWith(
      "/api/applications/{id}/variables",
      expect.objectContaining({ params: { path: { id: "app-1" } } }),
    );
    expect(screen.getByRole("button", { name: /^add$/i })).toBeInTheDocument();
  });

  it("is read-only for a viewer", async () => {
    role = "viewer";
    renderPage();
    expect(await screen.findByText("LOG_LEVEL")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^add$/i })).toBeNull();
  });

  it("goes back to the application page", async () => {
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /back to application/i }),
    );
    expect(await screen.findByText("application page")).toBeInTheDocument();
  });
});
