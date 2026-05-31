import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthGate } from "./AuthGate";

// Drive react-oidc-context's state by hand so we can assert AuthGate's
// branching without a real OIDC provider.
const signinRedirect = vi.fn();
let mockAuth: Record<string, unknown>;
let mockHasAuthParams = false;

vi.mock("react-oidc-context", () => ({
  useAuth: () => mockAuth,
  hasAuthParams: () => mockHasAuthParams,
}));

function renderGate() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route element={<AuthGate />}>
          <Route index element={<div>protected content</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  signinRedirect.mockClear();
  mockHasAuthParams = false;
  mockAuth = {
    isAuthenticated: false,
    isLoading: false,
    activeNavigator: undefined,
    error: undefined,
    signinRedirect,
  };
});

describe("AuthGate", () => {
  it("redirects to the IdP when unauthenticated and idle", () => {
    renderGate();
    expect(signinRedirect).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Signing in…")).toBeInTheDocument();
    expect(screen.queryByText("protected content")).not.toBeInTheDocument();
  });

  it("renders the protected outlet when authenticated", () => {
    mockAuth.isAuthenticated = true;
    renderGate();
    expect(signinRedirect).not.toHaveBeenCalled();
    expect(screen.getByText("protected content")).toBeInTheDocument();
  });

  it("does not redirect while the callback is still being processed", () => {
    // Mid-callback: ?code/&state present and the lib is loading.
    mockHasAuthParams = true;
    mockAuth.isLoading = true;
    renderGate();
    expect(signinRedirect).not.toHaveBeenCalled();
  });

  it("surfaces an error with a retry action instead of redirecting", () => {
    mockAuth.error = new Error("boom");
    renderGate();
    expect(signinRedirect).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });
});
