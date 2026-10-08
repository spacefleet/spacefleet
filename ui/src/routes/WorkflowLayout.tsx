import { Outlet } from "react-router";
import { WorkflowDraftProvider } from "../contexts/WorkflowDraftContext";

// WorkflowLayout owns the in-memory workflow draft for the nested workflow
// routes (the stage builder at the index, the full-page component editor under
// nodes/:nodeId). Because the whole workflow is saved with a single PUT and
// unsaved edits live in memory, the provider lives here — above the Outlet — so
// the draft survives navigation between the builder and the editor.
export function WorkflowLayout() {
  return (
    <WorkflowDraftProvider>
      <Outlet />
    </WorkflowDraftProvider>
  );
}
