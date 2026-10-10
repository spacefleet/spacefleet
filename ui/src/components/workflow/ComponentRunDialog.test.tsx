import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import { ComponentRunDialog } from "./ComponentRunDialog";

vi.mock("../../api/client", () => ({
  api: { POST: vi.fn() },
}));
const mockPost = api.POST as unknown as ReturnType<typeof vi.fn>;

const component = {
  id: "comp-1",
  name: "infra",
  config: { repo_url: "https://github.com/acme/infra.git", git_ref: "main" },
};

function renderDialog(
  overrides: Partial<Parameters<typeof ComponentRunDialog>[0]> = {},
) {
  const props = {
    appId: "app-1",
    component,
    onClose: vi.fn(),
    onBeforeRun: vi.fn().mockResolvedValue(null),
    onStarted: vi.fn(),
    ...overrides,
  };
  render(<ComponentRunDialog {...props} />);
  return props;
}

beforeEach(() => {
  mockPost.mockReset();
});

describe("ComponentRunDialog", () => {
  it("deploys the component at its own ref by default, saving pending edits first", async () => {
    mockPost.mockResolvedValue({ data: { id: "run-1" }, error: undefined });
    const props = renderDialog();
    // The ref field names the component's own ref as the fallback.
    expect(screen.getByLabelText("Git ref")).toHaveAttribute("placeholder", "main");
    // Targets stay out of the way until asked for.
    expect(screen.queryByLabelText("Target addresses")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(props.onStarted).toHaveBeenCalledWith("run-1"));
    expect(props.onBeforeRun).toHaveBeenCalled();
    expect(mockPost).toHaveBeenCalledWith(
      "/api/applications/{id}/components/{componentId}/runs",
      {
        params: { path: { id: "app-1", componentId: "comp-1" } },
        body: { action: "deploy" },
      },
    );
  });

  it("sends a git ref and the advanced targets when given", async () => {
    mockPost.mockResolvedValue({ data: { id: "run-2" }, error: undefined });
    const props = renderDialog({
      component: { ...component, config: { repo_url: "r" } },
    });
    expect(screen.getByLabelText("Git ref")).toHaveAttribute(
      "placeholder",
      "default branch",
    );
    fireEvent.change(screen.getByLabelText("Git ref"), {
      target: { value: " v1.2.0 " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Advanced" }));
    fireEvent.change(screen.getByLabelText("Target addresses"), {
      target: { value: " aws_instance.web\nmodule.vpc.aws_subnet.private[0], " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(props.onStarted).toHaveBeenCalledWith("run-2"));
    expect(mockPost).toHaveBeenCalledWith(
      "/api/applications/{id}/components/{componentId}/runs",
      {
        params: { path: { id: "app-1", componentId: "comp-1" } },
        body: {
          action: "deploy",
          git_ref: "v1.2.0",
          targets: ["aws_instance.web", "module.vpc.aws_subnet.private[0]"],
        },
      },
    );
  });

  it("does not start a run when saving pending edits fails", async () => {
    const props = renderDialog({
      onBeforeRun: vi.fn().mockResolvedValue("Could not save the workflow"),
    });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText("Could not save the workflow")).toBeInTheDocument();
    expect(mockPost).not.toHaveBeenCalled();
    expect(props.onStarted).not.toHaveBeenCalled();
  });

  it("shows the API's reason when the run is refused", async () => {
    mockPost.mockResolvedValue({
      data: undefined,
      error: { message: 'workflows: invalid git ref "a b": contains \' \'' },
      response: { status: 400 },
    });
    renderDialog();
    fireEvent.change(screen.getByLabelText("Git ref"), { target: { value: "a b" } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText(/invalid git ref/)).toBeInTheDocument();
  });

  it("says when another run is already in flight", async () => {
    mockPost.mockResolvedValue({
      data: undefined,
      error: { message: "conflict" },
      response: { status: 409 },
    });
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(
      await screen.findByText("A run is already in progress for this application."),
    ).toBeInTheDocument();
  });
});
