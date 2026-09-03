import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { PlanCounts, PlanResourceList, PlanSummaryBar } from "./PlanView";

type PlanSummary = components["schemas"]["PlanSummary"];

const plan: PlanSummary = {
  has_changes: true,
  add: 2,
  change: 1,
  destroy: 1,
  replace: 1,
  outputs_changed: true,
  resources: [
    {
      address: "aws_instance.web",
      action: "create",
      detail: "will be created",
      diff: '  # aws_instance.web will be created\n  + resource "aws_instance" "web" {\n      + ami = "ami-1"\n    }',
    },
    {
      address: "aws_db_instance.main",
      action: "replace",
      detail: "must be replaced",
      diff: '  # aws_db_instance.main must be replaced\n-/+ resource "aws_db_instance" "main" {\n      ~ engine_version = "14" -> "15" # forces replacement\n    }',
    },
    { address: "aws_subnet.a", action: "update", detail: "will be updated in-place" },
  ],
};

describe("PlanCounts", () => {
  it("shows add/change/destroy and the replace count", () => {
    render(<PlanCounts plan={plan} />);
    expect(screen.getByText("+2")).toBeInTheDocument();
    expect(screen.getByText("~1")).toBeInTheDocument();
    expect(screen.getByText("-1")).toBeInTheDocument();
    expect(screen.getByText("±1")).toBeInTheDocument();
  });

  it("reads 'no changes' for an empty plan", () => {
    render(
      <PlanCounts
        plan={{ has_changes: false, add: 0, change: 0, destroy: 0, replace: 0, resources: [] }}
      />,
    );
    expect(screen.getByText("no changes")).toBeInTheDocument();
  });
});

describe("PlanSummaryBar", () => {
  it("calls out destruction and output changes", () => {
    render(<PlanSummaryBar plan={plan} />);
    expect(
      screen.getByText("Plan: 2 to add, 1 to change, 1 to destroy."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("1 resource will be destroyed and recreated."),
    ).toBeInTheDocument();
    expect(screen.getByText("Output values change.")).toBeInTheDocument();
  });
});

describe("PlanResourceList", () => {
  it("orders the most destructive actions first and expands a row to its diff", () => {
    render(<PlanResourceList plan={plan} />);
    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("aws_db_instance.main"); // replace first
    expect(rows[1]).toHaveTextContent("aws_subnet.a"); // then update
    expect(rows[2]).toHaveTextContent("aws_instance.web"); // then create

    // The diff is hidden until the row is expanded.
    expect(screen.queryByText(/forces replacement/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /aws_db_instance.main/ }));
    expect(screen.getByText(/forces replacement/)).toBeInTheDocument();
  });

  it("renders a row without a diff (below editor) as non-expandable", () => {
    render(<PlanResourceList plan={plan} />);
    const row = screen.getByRole("button", { name: /aws_subnet.a/ });
    expect(row).toBeDisabled();
    expect(row).not.toHaveAttribute("aria-expanded");
  });
});
