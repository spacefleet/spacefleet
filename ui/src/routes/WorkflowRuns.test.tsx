import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useSearchParams } from "react-router";
import { describe, expect, it } from "vitest";
import { WorkflowRuns } from "./WorkflowRuns";

function RunsProbe() {
  const [params] = useSearchParams();
  return <div>runs index for {params.get("application")}</div>;
}

// WorkflowRuns is now a redirect to the global run index, pre-filtered to the
// application — run history lives in one place (Applications › Workflow Runs).
function renderRedirect() {
  return render(
    <MemoryRouter initialEntries={["/applications/app-1/runs"]}>
      <Routes>
        <Route path="/applications/:appId/runs" element={<WorkflowRuns />} />
        <Route path="/runs" element={<RunsProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("WorkflowRuns", () => {
  it("redirects to the global run index pre-filtered to the application", async () => {
    renderRedirect();
    expect(await screen.findByText("runs index for app-1")).toBeInTheDocument();
  });
});
