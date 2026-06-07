import { Navigate, useParams } from "react-router";

// WorkflowRuns previously rendered a single application's run history. Run
// history now lives in the global index (Applications › Workflow Runs), which can
// be pre-filtered to one application — so there's a single source of truth rather
// than a duplicate per-app table. This route (/applications/:appId/runs) redirects
// there pre-filtered to the application, keeping older links working.
export function WorkflowRuns() {
  const { appId = "" } = useParams();
  return <Navigate to={`/runs?application=${appId}`} replace />;
}
