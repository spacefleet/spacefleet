import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RepositoryPicker } from "./RepositoryPicker";
import { api } from "../../api/client";

vi.mock("../../api/client", () => ({
  api: { GET: vi.fn() },
}));

const mockApi = api as unknown as { GET: ReturnType<typeof vi.fn> };

const repos = [
  {
    installation_id: "inst-acme",
    account_login: "acme",
    full_name: "acme/charts",
    clone_url: "https://github.com/acme/charts.git",
    default_branch: "main",
    private: true,
  },
  {
    installation_id: "inst-acme",
    account_login: "acme",
    full_name: "acme/infra",
    clone_url: "https://github.com/acme/infra.git",
    default_branch: "main",
    private: true,
  },
  {
    installation_id: "inst-kyle",
    account_login: "kyle",
    full_name: "kyle/manifests",
    clone_url: "https://github.com/kyle/manifests.git",
    default_branch: "main",
    private: false,
  },
];

beforeEach(() => {
  mockApi.GET.mockReset();
});

describe("RepositoryPicker", () => {
  it("fetches lazily: nothing is requested until the button is clicked", async () => {
    mockApi.GET.mockResolvedValue({ data: repos, error: undefined });
    render(<RepositoryPicker onSelect={() => {}} />);
    expect(mockApi.GET).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /select repository/i }));
    await waitFor(() =>
      expect(mockApi.GET).toHaveBeenCalledWith("/api/github/repositories"),
    );
  });

  it("groups repositories by account and filters on search", async () => {
    mockApi.GET.mockResolvedValue({ data: repos, error: undefined });
    render(<RepositoryPicker onSelect={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: /select repository/i }));

    expect(await screen.findByText("acme/charts")).toBeInTheDocument();
    expect(screen.getByText("kyle/manifests")).toBeInTheDocument();
    // Account group headers are present.
    expect(screen.getByText("acme")).toBeInTheDocument();
    expect(screen.getByText("kyle")).toBeInTheDocument();

    await userEvent.type(
      screen.getByPlaceholderText(/search repositories/i),
      "infra",
    );
    expect(screen.getByText("acme/infra")).toBeInTheDocument();
    expect(screen.queryByText("acme/charts")).not.toBeInTheDocument();
    expect(screen.queryByText("kyle/manifests")).not.toBeInTheDocument();
  });

  it("calls onSelect with the picked repo and closes the modal", async () => {
    mockApi.GET.mockResolvedValue({ data: repos, error: undefined });
    const onSelect = vi.fn();
    render(<RepositoryPicker onSelect={onSelect} />);
    await userEvent.click(screen.getByRole("button", { name: /select repository/i }));

    await userEvent.click(await screen.findByText("acme/infra"));
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({
        full_name: "acme/infra",
        clone_url: "https://github.com/acme/infra.git",
        installation_id: "inst-acme",
      }),
    );
    // Modal closed — the search box is gone.
    expect(
      screen.queryByPlaceholderText(/search repositories/i),
    ).not.toBeInTheDocument();
  });

  it("shows an empty state when no repositories are reachable", async () => {
    mockApi.GET.mockResolvedValue({ data: [], error: undefined });
    render(<RepositoryPicker onSelect={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: /select repository/i }));
    expect(await screen.findByText(/no repositories found/i)).toBeInTheDocument();
  });
});
