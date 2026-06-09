import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft, Save, Trash2 } from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import type { components } from "../api/schema";
import {
  ComponentFields,
  type EditableComponent,
} from "../components/workflow/ComponentFields";
import { VariablesEditor } from "../components/VariablesEditor";

type ComponentType = components["schemas"]["ComponentType"];

// The component types the create route (?new=…) accepts, so a hand-edited or
// stale URL can't seed a bogus node.
const NEW_TYPES: ComponentType[] = ["helm", "manifest", "terraform"];

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
  const { appId = "", nodeId = "" } = useParams();
  const [searchParams] = useSearchParams();
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
    ensureProvisional,
    commitComponent,
    discardNewNode,
    deleteNode,
  } = useWorkflowDraft();

  // The committed node as it currently lives in the shared draft.
  const committed = getComponent(nodeId);
  const isNew = isProvisional(nodeId);

  // Create flow: when the route carries ?new=<type> and the node isn't in the
  // draft yet, seed it. This runs both on the first add (addComponent navigates
  // here) and after a page reload of the create page — which re-fetches the
  // workflow without the still-unsaved node — so the reload re-seeds a fresh
  // create form instead of "node not found". Gated on !loading so it fires after
  // the workflow load settles (not against the pre-load empty draft).
  //
  // discardedRef stops the seed from coming back to life after the user cancels:
  // discardNewNode removes the node, which re-fires this effect (committed flips
  // to null) and would otherwise recreate what we just discarded. It is NOT keyed
  // on having-seeded-once, because a legitimate re-seed is required when a draft
  // reload wipes the provisional node — the org context loads asynchronously and
  // can trigger a second workflow load that resets the draft right after we seed,
  // so the effect must re-seed then. cancel()/removeNode() set this before they
  // discard; a page reload remounts the editor fresh, resetting it.
  const newType = searchParams.get("new");
  const discardedRef = useRef(false);
  useEffect(() => {
    if (loading || error) return;
    if (committed || discardedRef.current) return;
    if (newType && (NEW_TYPES as string[]).includes(newType)) {
      ensureProvisional(nodeId, newType as ComponentType);
    }
  }, [loading, error, committed, newType, nodeId, ensureProvisional]);

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
  // leave (the local edits were never applied to the draft). discardedRef stops
  // the create effect from re-seeding the node we're discarding before the
  // navigation away unmounts the editor.
  function cancel() {
    if (isNew) {
      discardedRef.current = true;
      discardNewNode(nodeId);
    }
    backToWorkflow();
  }

  function saveNode() {
    if (!draft) return;
    commitComponent(draft);
    backToWorkflow();
  }

  function removeNode() {
    if (isNew) {
      discardedRef.current = true;
      discardNewNode(nodeId);
    } else {
      deleteNode(nodeId);
    }
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

          {/* Component variables. These override the app-level variables of the
              same name for this component's job, and save through their own
              endpoints (separately from the node config above) — so they're only
              available once the node has been saved into the workflow. */}
          <div className="mt-6 border border-neutral-200 bg-white p-4">
            <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-400">
              Variables
            </h2>
            <p className="mb-3 mt-1 text-xs text-neutral-500">
              Passed to this component’s job as environment variables, overriding
              any app-level variable of the same name. Saved separately from the
              node above; a sensitive value is sealed and never shown again.
            </p>
            {isNew ? (
              <p className="text-sm text-neutral-500">
                Save the node first to add component variables.
              </p>
            ) : (
              <VariablesEditor
                scope={{ kind: "component", appId, componentId: nodeId }}
                canEdit={canEdit}
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}
