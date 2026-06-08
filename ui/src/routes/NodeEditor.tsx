import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft, Save, Trash2 } from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import {
  ComponentFields,
  type EditableComponent,
} from "../components/workflow/ComponentFields";

// componentsEqual compares two editable components field by field (config is a
// flat string map) so the editor can tell whether its local working copy still
// matches the committed node — which drives the unsaved-changes hint and whether
// Save is enabled.
function componentsEqual(a: EditableComponent, b: EditableComponent): boolean {
  if (
    a.name !== b.name ||
    a.type !== b.type ||
    a.continue_on_failure !== b.continue_on_failure ||
    a.requires_approval !== b.requires_approval ||
    a.target_cluster_id !== b.target_cluster_id ||
    a.target_namespace !== b.target_namespace ||
    a.chart_credential_id !== b.chart_credential_id ||
    a.github_installation_id !== b.github_installation_id
  ) {
    return false;
  }
  const ak = Object.keys(a.config);
  const bk = Object.keys(b.config);
  if (ak.length !== bk.length) return false;
  return ak.every((k) => a.config[k] === b.config[k]);
}

// NodeEditor is the full-page editor for one workflow node, reached at
// /applications/:appId/workflow/nodes/:nodeId. It edits a local working copy of
// the node — edits do NOT touch the shared workflow draft until the user clicks
// Save. Cancel (or Back) discards those edits; for a freshly added node, which is
// provisional until saved, Cancel removes it from the draft entirely. Saving a
// node is therefore its own operation, separate from the canvas's layout save.
// Viewers see the fields read-only with no Save/Cancel. A Back link returns to
// the canvas through the router (never raw history).
export function NodeEditor() {
  const { nodeId = "" } = useParams();
  const navigate = useNavigate();
  const {
    canEdit,
    loading,
    error,
    clusters,
    credentials,
    cloudCredentials,
    installations,
    githubEnabled,
    getComponent,
    isProvisional,
    commitComponent,
    discardNewNode,
    deleteNode,
  } = useWorkflowDraft();

  // The committed node as it currently lives in the shared draft.
  const committed = getComponent(nodeId);
  const isNew = isProvisional(nodeId);

  // Local working copy. Seeded once per node id from the committed value; edits
  // stay here until Save. (Local edits never call updateComponent, so `committed`
  // keeps a stable identity and this won't clobber in-progress edits.)
  const [draft, setDraft] = useState<EditableComponent | null>(committed);
  const seededFor = useRef<string | null>(committed ? nodeId : null);
  useEffect(() => {
    if (committed && seededFor.current !== nodeId) {
      setDraft(committed);
      seededFor.current = nodeId;
    }
  }, [committed, nodeId]);

  function backToWorkflow() {
    // Relative navigation up to the canvas (index of the workflow layout).
    navigate("..");
  }

  // Cancel/Back: drop a provisional node entirely; for a committed node just
  // leave (the local edits were never applied to the draft).
  function cancel() {
    if (isNew) discardNewNode(nodeId);
    backToWorkflow();
  }

  function saveNode() {
    if (!draft) return;
    commitComponent(draft);
    backToWorkflow();
  }

  function removeNode() {
    if (isNew) discardNewNode(nodeId);
    else deleteNode(nodeId);
    backToWorkflow();
  }

  const view = draft ?? committed;
  const dirty =
    draft != null && committed != null && !componentsEqual(draft, committed);
  // A provisional node always has something to save (it isn't persisted yet).
  const hasUnsaved = canEdit && (dirty || isNew);

  return (
    <div className="mx-auto flex max-w-4xl flex-col">
      <button
        type="button"
        onClick={cancel}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to workflow
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-600">{error}</p>
      ) : !view ? (
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
                {view.type} component
                {isNew && <span className="ml-1 text-neutral-400">· new</span>}
              </p>
              <h1 className="mt-0.5 truncate text-xl font-bold tracking-tight">
                {view.name || "Edit node"}
              </h1>
            </div>
            {canEdit && (
              <button
                type="button"
                onClick={removeNode}
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
              component={view}
              onChange={setDraft}
              clusters={clusters}
              credentials={credentials}
              cloudCredentials={cloudCredentials}
              installations={installations}
              githubEnabled={githubEnabled}
              disabled={!canEdit}
            />
          </div>

          {canEdit && (
            <div className="mt-4 flex items-center justify-end gap-3">
              <span className="mr-auto text-xs text-neutral-400">
                {hasUnsaved
                  ? "Unsaved changes"
                  : "Saved to workflow"}
              </span>
              <button
                type="button"
                onClick={cancel}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={saveNode}
                disabled={!hasUnsaved}
                className="inline-flex items-center gap-1.5 bg-black px-4 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
              >
                <Save className="h-3.5 w-3.5" />
                Save node
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
