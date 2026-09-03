import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { ResourcesTable } from "./ResourcesTable";

type TofuResource = components["schemas"]["TofuResource"];

const resources: TofuResource[] = [
  { address: "data.aws_ami.ubuntu", mode: "data", type: "aws_ami", name: "ubuntu", provider: "registry.opentofu.org/hashicorp/aws" },
  { address: "module.net.aws_vpc.main", mode: "managed", type: "aws_vpc", name: "main", provider: "registry.opentofu.org/hashicorp/aws", id: "vpc-1" },
  { address: "aws_s3_bucket.logs", mode: "managed", type: "aws_s3_bucket", name: "logs", provider: "registry.opentofu.org/hashicorp/aws", id: "acme-logs" },
];

describe("ResourcesTable", () => {
  it("lists managed resources before data sources with short provider names and ids", () => {
    render(<ResourcesTable resources={resources} />);
    const rows = screen.getAllByRole("row").slice(1); // skip the header
    expect(rows[0]).toHaveTextContent("module.net.aws_vpc.main");
    expect(rows[0]).toHaveTextContent("vpc-1");
    expect(rows[0]).toHaveTextContent("aws"); // provider trimmed to its last segment
    expect(rows[2]).toHaveTextContent("data.aws_ami.ubuntu"); // data source last
    expect(screen.getByText("2 managed resources · 1 data source")).toBeInTheDocument();
  });

  it("filters a long inventory by any identity field", () => {
    const many: TofuResource[] = Array.from({ length: 12 }, (_, i) => ({
      address: `aws_instance.web[${i}]`,
      mode: "managed",
      type: "aws_instance",
      name: "web",
      provider: "registry.opentofu.org/hashicorp/aws",
      id: `i-${i}`,
    }));
    render(<ResourcesTable resources={[...many, ...resources]} />);
    fireEvent.change(screen.getByLabelText("Filter resources"), {
      target: { value: "acme-logs" },
    });
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("aws_s3_bucket.logs");
  });

  it("says so when the state holds nothing", () => {
    render(<ResourcesTable resources={[]} />);
    expect(
      screen.getByText("No resources are recorded in this component's state."),
    ).toBeInTheDocument();
  });
});
