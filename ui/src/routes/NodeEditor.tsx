import { useNavigate, useParams } from "react-router";
import { ArrowLeft, Trash2 } from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import { ComponentFields } from "../components/workflow/ComponentFields";

// NodeEditor is the full-page editor for one workflow node, reached at
// /applications/:appId/workflow/nodes/:nodeId. It reads the node from the shared
// workflow draft, edits flow back into the draft via updateComponent (the PUT
// happens on the canvas's Save), and a Back link returns to the canvas through
// the router (never raw history). Viewers see the fields read-only. The wider
// layout lays the form out in a roomy two-column grid rather than the old
// cramped side panel.
export function NodeEditor() {
  const { nodeId = "" } = useParams();
  const navigate = useNavigate();
  const {
    canEdit,
    loading,
    error,
    clusters,
    credentials,
    installations,
    githubEnabled,
    getComponent,
    updateComponent,
    deleteNode,
  } = useWorkflowDraft();

  const component = getComponent(nodeId);

  function backToWorkflow() {
    // Relative navigation up to the canvas (index of the workflow layout).
    navigate("..");
  }

  return (
    <div className="mx-auto flex max-w-4xl flex-col">
      <button
        type="button"
        onClick={backToWorkflow}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to workflow
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : !component ? (
        <div className="mt-6">
          <p className="text-sm text-neutral-600">
            That node isn’t in this workflow.
          </p>
          <button
            type="button"
            onClick={backToWorkflow}
            className="mt-2 text-sm font-medium text-neutral-700 hover:text-black"
          >
            Back to the canvas
          </button>
        </div>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-3 pb-4">
            <div className="min-w-0">
              <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
                {component.type} component
              </p>
              <h1 className="mt-0.5 truncate text-xl font-bold tracking-tight">
                {component.name || "Edit node"}
              </h1>
            </div>
            {canEdit && (
              <button
                type="button"
                onClick={() => {
                  deleteNode(component.id);
                  backToWorkflow();
                }}
                className="inline-flex shrink-0 items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Delete node
              </button>
            )}
          </div>

          {!canEdit && (
            <p className="mb-4 border border-neutral-200 bg-neutral-50 px-3 py-2 text-sm text-neutral-600">
              You have view-only access to this workflow.
            </p>
          )}

          <div className="border border-neutral-200 bg-white p-6">
            <ComponentFields
              component={component}
              onChange={updateComponent}
              clusters={clusters}
              credentials={credentials}
              installations={installations}
              githubEnabled={githubEnabled}
              disabled={!canEdit}
            />
          </div>
        </>
      )}
    </div>
  );
}
