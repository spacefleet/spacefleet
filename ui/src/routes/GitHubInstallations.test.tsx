import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GitHubInstallations } from "./GitHubInstallations";
import { api } from "../api/client";

vi.mock("../api/client", () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}));

vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({
    currentOrg: { id: "org-1", name: "Acme" },
    currentRole: "owner",
  }),
}));

const mockApi = api as unknown as {
  GET: ReturnType<typeof vi.fn>;
  DELETE: ReturnType<typeof vi.fn>;
};

const oneInstallation = {
  id: "inst-1",
  installation_id: 12345,
  account_login: "acme",
  account_type: "Organization",
  created_at: "2026-06-03T00:00:00Z",
  updated_at: "2026-06-03T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/admin/github"]}>
      <GitHubInstallations />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.GET.mockReset();
  mockApi.DELETE.mockReset();
  window.appConfig = {
    oidcIssuer: "",
    oidcClientId: "",
    loginMethods: [],
    allowOrgCreation: true,
    emailEnabled: false,
    githubAppEnabled: true,
  };
});

describe("GitHubInstallations", () => {
  it("shows the empty state when there are no installations", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderPage();
    expect(
      await screen.findByText("No GitHub installations yet"),
    ).toBeInTheDocument();
  });

  it("renders installations with their account and id", async () => {
    mockApi.GET.mockResolvedValue({
      data: [oneInstallation],
      error: undefined,
    });
    renderPage();
    expect(await screen.findByText("acme")).toBeInTheDocument();
    expect(screen.getByText("12345")).toBeInTheDocument();
  });

  it("redirects to the connect URL when Connect GitHub is clicked", async () => {
    mockApi.GET.mockImplementation((path: string) => {
      if (path === "/api/github/installations/connect-url") {
        return Promise.resolve({
          data: { url: "https://github.com/apps/spacefleet/installations/new?state=abc" },
          error: undefined,
        });
      }
      return Promise.resolve({ data: [], error: undefined });
    });
    // jsdom's window.location isn't assignable by default; stub a setter.
    const hrefSpy = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { set href(v: string) { hrefSpy(v); } },
    });
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /connect github/i }),
    );
    await waitFor(() =>
      expect(hrefSpy).toHaveBeenCalledWith(
        "https://github.com/apps/spacefleet/installations/new?state=abc",
      ),
    );
  });

  it("hides Connect GitHub and warns when no app is configured", async () => {
    window.appConfig.githubAppEnabled = false;
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    renderPage();
    expect(
      await screen.findByText(/no github app is configured/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /connect github/i }),
    ).not.toBeInTheDocument();
  });
});
