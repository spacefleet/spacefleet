import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Login } from "./Login";

// Drive react-oidc-context's state by hand. Login only needs an idle,
// unauthenticated session plus a signinRedirect spy.
const signinRedirect = vi.fn();
let mockAuth: Record<string, unknown>;

vi.mock("react-oidc-context", () => ({
  useAuth: () => mockAuth,
}));

function renderLogin() {
  return render(
    <MemoryRouter>
      <Login />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  signinRedirect.mockClear();
  mockAuth = {
    isAuthenticated: false,
    isLoading: false,
    activeNavigator: undefined,
    error: undefined,
    signinRedirect,
  };
  window.appConfig = {
    oidcIssuer: "",
    oidcClientId: "",
    loginMethods: [],
    allowOrgCreation: true,
    emailEnabled: false,
    githubAppEnabled: false,
  };
});

describe("Login", () => {
  it("renders one button per configured method, deep-linking to its connector", async () => {
    window.appConfig.loginMethods = [
      { id: "github", name: "GitHub", type: "github" },
      { id: "local", name: "Email and password", type: "password" },
    ];
    renderLogin();

    const github = screen.getByRole("button", { name: "Continue with GitHub" });
    const local = screen.getByRole("button", {
      name: "Continue with Email and password",
    });
    expect(github).toBeInTheDocument();
    expect(local).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Sign in" }),
    ).not.toBeInTheDocument();

    github.click();
    expect(signinRedirect).toHaveBeenCalledWith({
      extraQueryParams: { connector_id: "github" },
    });
  });

  it("falls back to a single generic Sign in button when no methods are configured", () => {
    renderLogin();

    const signIn = screen.getByRole("button", { name: "Sign in" });
    expect(signIn).toBeInTheDocument();

    signIn.click();
    expect(signinRedirect).toHaveBeenCalledWith();
  });
});
