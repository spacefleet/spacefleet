import { Outlet } from "react-router";
import { ReactFlowProvider } from "@xyflow/react";
import { WorkflowDraftProvider } from "../contexts/WorkflowDraftContext";

// WorkflowLayout owns the in-memory workflow draft for the nested workflow
// routes (the canvas at the index, the full-page node editor under
// nodes/:nodeId). Because the whole workflow is saved with a single PUT and
// unsaved edits live in memory, the provider lives here — above the Outlet — so
// the draft survives navigation between the canvas and the editor. ReactFlowProvider
// wraps it so the canvas (and the draft, indirectly) can drive the shared React
// Flow viewport instance — e.g. to fit newly added nodes into view.
export function WorkflowLayout() {
  return (
    <ReactFlowProvider>
      <WorkflowDraftProvider>
        <Outlet />
      </WorkflowDraftProvider>
    </ReactFlowProvider>
  );
}
