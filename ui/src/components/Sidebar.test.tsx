import { render, screen, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "./Sidebar";

let role = "editor";
vi.mock("../contexts/OrgContext", () => ({
  useOrg: () => ({ currentRole: role }),
}));

// LocationProbe exposes the router's current path so a test can see where a
// sidebar click landed.
function LocationProbe() {
  return <p data-testid="location">{useLocation().pathname}</p>;
}

function renderSidebar(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Sidebar />
      <LocationProbe />
    </MemoryRouter>,
  );
}

const switcher = () => screen.getByTestId("area-switcher");
const railLinks = () =>
  within(screen.getByRole("navigation", { name: "Primary" }))
    .getAllByRole("link")
    .map((l) => l.textContent);

const adminLinks = [
  "Members",
  "Clusters",
  "Cloud Credentials",
  "Private Charts",
  "GitHub",
  "Notifications",
  "Policies",
];

beforeEach(() => {
  role = "editor";
});

describe("Sidebar", () => {
  it("lands on Overview, whose rail lists the other areas", () => {
    renderSidebar("/");
    expect(switcher()).toHaveTextContent("Overview");
    expect(railLinks()).toEqual(["Applications", "Clusters"]);
  });

  it("switches to an area's pages when following a rail link into it", async () => {
    renderSidebar("/");
    await userEvent.click(screen.getByRole("link", { name: "Applications" }));

    expect(screen.getByTestId("location")).toHaveTextContent("/applications");
    expect(switcher()).toHaveTextContent("Applications");
    expect(railLinks()).toEqual(["All Apps", "Workflow Runs"]);
    expect(screen.getByRole("link", { name: "All Apps" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("navigates to an area's landing page from the switcher", async () => {
    renderSidebar("/applications");
    await userEvent.click(switcher());
    expect(
      screen.getByRole("menuitemradio", { name: /Applications/ }),
    ).toHaveAttribute("aria-checked", "true");
    await userEvent.click(screen.getByRole("menuitemradio", { name: /Clusters/ }));

    expect(screen.getByTestId("location")).toHaveTextContent(
      "/clusters/namespaces",
    );
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(switcher()).toHaveTextContent("Clusters");
    expect(railLinks()).toEqual(["Nodes", "Namespaces", "Pods"]);
    expect(screen.getByRole("link", { name: "Namespaces" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("highlights the leaf a detail page sits under", () => {
    renderSidebar("/clusters/nodes/c1/node-a");
    expect(switcher()).toHaveTextContent("Clusters");
    expect(screen.getByRole("link", { name: "Nodes" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Namespaces" })).not.toHaveAttribute(
      "aria-current",
    );
  });

  it("hides Admin from non-admins", () => {
    renderSidebar("/");
    expect(
      screen.queryByRole("button", { name: "Admin" }),
    ).not.toBeInTheDocument();
  });

  it("keeps the current area while on an Admin page", async () => {
    role = "admin";
    renderSidebar("/clusters/namespaces");
    await userEvent.click(screen.getByRole("button", { name: "Admin" }));
    // Admin's own Clusters page (registration) is not part of the area.
    await userEvent.click(screen.getByRole("link", { name: "Clusters" }));

    expect(screen.getByTestId("location")).toHaveTextContent("/admin/clusters");
    expect(switcher()).toHaveTextContent("Clusters");
    expect(railLinks()).toEqual(["Nodes", "Namespaces", "Pods", ...adminLinks]);
    expect(screen.getByRole("link", { name: "Clusters" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });
});
