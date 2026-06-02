import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthGate } from "./AuthGate";

// Drive react-oidc-context's state by hand so we can assert AuthGate's
// branching without a real OIDC provider.
let mockAuth: Record<string, unknown>;

vi.mock("react-oidc-context", () => ({
  useAuth: () => mockAuth,
  hasAuthParams: () => false,
}));

// Render AuthGate over a protected index route, with a sibling /login route
// standing in for the real login screen so we can assert the redirect lands
// there (AuthGate no longer launches the Dex flow itself).
function renderGate() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route element={<AuthGate />}>
          <Route index element={<div>protected content</div>} />
        </Route>
        <Route path="/login" element={<div>login screen</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockAuth = {
    isAuthenticated: false,
    isLoading: false,
    activeNavigator: undefined,
    error: undefined,
  };
});

describe("AuthGate", () => {
  it("redirects to /login when unauthenticated and idle", () => {
    renderGate();
    expect(screen.getByText("login screen")).toBeInTheDocument();
    expect(screen.queryByText("protected content")).not.toBeInTheDocument();
  });

  it("renders the protected outlet when authenticated", () => {
    mockAuth.isAuthenticated = true;
    renderGate();
    expect(screen.getByText("protected content")).toBeInTheDocument();
    expect(screen.queryByText("login screen")).not.toBeInTheDocument();
  });

  it("shows a placeholder while the session is still loading", () => {
    mockAuth.isLoading = true;
    renderGate();
    expect(screen.getByText("Signing in…")).toBeInTheDocument();
    expect(screen.queryByText("login screen")).not.toBeInTheDocument();
    expect(screen.queryByText("protected content")).not.toBeInTheDocument();
  });

  it("sends sign-in errors to the login screen", () => {
    mockAuth.error = new Error("boom");
    renderGate();
    expect(screen.getByText("login screen")).toBeInTheDocument();
  });
});
