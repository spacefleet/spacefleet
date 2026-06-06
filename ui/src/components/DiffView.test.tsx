import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DiffView } from "./DiffView";

describe("DiffView", () => {
  it("colors added, removed, and header lines", () => {
    const diff = [
      "apps, web, Deployment (apps) has changed:",
      "- replicas: 2",
      "+ replicas: 3",
      "  unchanged: true",
    ].join("\n");
    const { container } = render(<DiffView diff={diff} />);
    const rows = container.querySelectorAll("div");
    expect(rows[0].className).toContain("font-medium"); // header
    expect(rows[1].className).toContain("text-red-400"); // removal
    expect(rows[2].className).toContain("text-green-400"); // addition
    expect(rows[3].className).toBe(""); // context line, neutral
  });
});
