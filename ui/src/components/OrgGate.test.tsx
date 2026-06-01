import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgGate } from "./OrgGate";

// Drive the org context by hand so we can assert OrgGate's branching without a
// real provider or network. NoOrganizations pulls from react-oidc-context, so
// stub that too.
let mockOrg: { loading: boolean; memberships: unknown[] };

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => mockOrg,
}));

vi.mock("react-oidc-context", () => ({
  useAuth: () => ({ user: undefined, removeUser: vi.fn() }),
}));

function renderGate() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route element={<OrgGate />}>
          <Route index element={<div>protected content</div>} />
        </Route>
        <Route path="/organizations/new" element={<div>create org screen</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockOrg = { loading: false, memberships: [] };
  window.appConfig = {
    oidcIssuer: "",
    oidcClientId: "",
    allowOrgCreation: true,
  };
});

afterEach(() => {
  // @ts-expect-error -- reset the runtime config between tests.
  delete window.appConfig;
});

describe("OrgGate", () => {
  it("renders the app when the user has a membership", () => {
    mockOrg = { loading: false, memberships: [{}] };
    renderGate();
    expect(screen.getByText("protected content")).toBeInTheDocument();
  });

  it("sends org-less users to create-org when creation is enabled", () => {
    renderGate();
    expect(screen.getByText("create org screen")).toBeInTheDocument();
  });

  it("shows the request-an-invite message when creation is disabled", () => {
    window.appConfig.allowOrgCreation = false;
    renderGate();
    expect(screen.getByText(/don't belong to any organizations/i)).toBeInTheDocument();
    expect(screen.queryByText("create org screen")).not.toBeInTheDocument();
  });
});
