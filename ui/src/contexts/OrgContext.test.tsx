import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { OrgProvider } from "./OrgContext";
import { OrgGate } from "../components/OrgGate";
import { Layout } from "../components/Layout";

// Drive the API client by hand so we control what /api/me returns.
const get = vi.fn();
const post = vi.fn();
vi.mock("../api/client", () => ({
  api: {
    GET: (...args: unknown[]) => get(...args),
    POST: (...args: unknown[]) => post(...args),
  },
  setOrgProvider: vi.fn(),
}));

// Layout depends on react-oidc-context; stub it.
vi.mock("react-oidc-context", () => ({
  useAuth: () => ({ user: { profile: { email: "dev@example.com" } }, removeUser: vi.fn() }),
}));

function membership(id: string, name: string, role = "member") {
  return { organization: { id, name, created_at: "", updated_at: "" }, role };
}

function renderApp() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route element={<OrgProvider />}>
          <Route path="/organizations/new" element={<div>create org screen</div>} />
          <Route element={<OrgGate />}>
            <Route element={<Layout />}>
              <Route index element={<div>protected content</div>} />
            </Route>
          </Route>
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  localStorage.clear();
});

describe("OrgGate + OrgSwitcher", () => {
  it("redirects to the create-org screen when the user has no organizations", async () => {
    get.mockResolvedValue({ data: { user: { id: "u1", email: "x" }, organizations: [] } });
    renderApp();
    expect(await screen.findByText("create org screen")).toBeInTheDocument();
    expect(screen.queryByText("protected content")).not.toBeInTheDocument();
  });

  it("renders the app and lists organizations in the switcher", async () => {
    get.mockResolvedValue({
      data: {
        user: { id: "u1", email: "x" },
        organizations: [membership("o1", "Acme", "owner"), membership("o2", "Globex")],
      },
    });
    renderApp();

    // App renders, defaulting to the first org.
    expect(await screen.findByText("protected content")).toBeInTheDocument();

    // Opening the switcher reveals both organizations. The current org is
    // selected in an effect that runs a tick after Layout renders, so the
    // trigger's label flips from its placeholder to "Acme" asynchronously —
    // find it (retries) rather than get it (one shot) to avoid a CI race.
    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));
    expect(screen.getByRole("menuitem", { name: /Globex/ })).toBeInTheDocument();

    // Switching persists the selection.
    await userEvent.click(screen.getByRole("menuitem", { name: /Globex/ }));
    await waitFor(() =>
      expect(localStorage.getItem("spacefleet.currentOrgId")).toBe("o2"),
    );
  });
});
