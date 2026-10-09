import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft, Save, Trash2 } from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import type { components } from "../api/schema";
import {
  ComponentFields,
  type EditableComponent,
} from "../components/workflow/ComponentFields";
import {
  componentsReferencing,
  upstreamTofuNames,
} from "../components/workflow/outputsRefs";
import type {
  OutputKeyInfo,
  RefContext,
} from "../components/workflow/refAutocomplete";
import { VariablesEditor } from "../components/VariablesEditor";
import { ComponentStatePanel } from "../components/workflow/ComponentStatePanel";
import { useDocumentTitle } from "../lib/useDocumentTitle";

type ComponentType = components["schemas"]["ComponentType"];

// The component types the create route (?new=…) accepts, so a hand-edited or
// stale URL can't seed a bogus component.
const NEW_TYPES: ComponentType[] = ["helm", "manifest", "terraform"];

// componentsEqual compares two editable components field by field (config is a
// flat string map) so the editor can tell whether its local working copy still
// matches the committed component — which drives the unsaved-changes hint and
// whether Save is enabled.
function componentsEqual(a: EditableComponent, b: EditableComponent): boolean {
  if (
    a.name !== b.name ||
    a.type !== b.type ||
    a.continue_on_failure !== b.continue_on_failure ||
    a.requires_approval !== b.requires_approval ||
    JSON.stringify(a.approval_policy ?? null) !==
      JSON.stringify(b.approval_policy ?? null) ||
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

// NodeEditor is the full-page editor for one workflow component, reached at
// /applications/:appId/workflow/nodes/:nodeId. It edits a local working copy of
// the component — edits do NOT touch the shared workflow draft until the user
// clicks Save. Cancel (or Back) discards those edits; for a freshly added
// component, which is provisional until saved, Cancel removes it from the draft
// entirely. Viewers see the fields read-only with no Save/Cancel. A Back link
// returns to the stage builder through the router (never raw history).
export function NodeEditor() {
  const { appId = "", nodeId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const {
    appName,
    canEdit,
    loading,
    error,
    stages,
    stageOf,
    clusters,
    credentials,
    cloudCredentials,
    installations,
    githubEnabled,
    appVariableNames,
    componentOutputs,
    getComponent,
    isProvisional,
    ensureProvisional,
    commitComponent,
    discardNewNode,
    deleteComponent,
  } = useWorkflowDraft();

  // The committed component as it currently lives in the shared draft, and the
  // stage it runs in.
  const committed = getComponent(nodeId);
  const isNew = isProvisional(nodeId);
  const placed = stageOf(nodeId);
  useDocumentTitle(committed?.name, "Workflow", appName);

  // Create flow: when the route carries ?new=<type> (and ?stage=<id>) and the
  // component isn't in the draft yet, seed it in that stage. This runs both on
  // the first add (addComponent navigates here) and after a page reload of the
  // create page — which re-fetches the workflow without the still-unsaved
  // component — so the reload re-seeds a fresh create form instead of "not
  // found". Gated on !loading so it fires after the workflow load settles (not
  // against the pre-load empty draft).
  //
  // discardedRef stops the seed from coming back to life after the user cancels:
  // discardNewNode removes the component, which re-fires this effect (committed
  // flips to null) and would otherwise recreate what we just discarded. It is NOT
  // keyed on having-seeded-once, because a legitimate re-seed is required when a
  // draft reload wipes the provisional component — the org context loads
  // asynchronously and can trigger a second workflow load that resets the draft
  // right after we seed, so the effect must re-seed then. cancel()/removeNode()
  // set this before they discard; a page reload remounts the editor fresh,
  // resetting it.
  const newType = searchParams.get("new");
  const newStage = searchParams.get("stage");
  const discardedRef = useRef(false);
  useEffect(() => {
    if (loading || error) return;
    if (committed || discardedRef.current) return;
    if (newType && (NEW_TYPES as string[]).includes(newType)) {
      ensureProvisional(nodeId, newType as ComponentType, newStage);
    }
  }, [loading, error, committed, newType, newStage, nodeId, ensureProvisional]);

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
    // Relative navigation up to the builder (index of the workflow layout).
    navigate("..");
  }

  // Cancel/Back: drop a provisional component entirely; for a committed one just
  // leave (the local edits were never applied to the draft). discardedRef stops
  // the create effect from re-seeding the component we're discarding before the
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
      deleteComponent(nodeId);
    }
    backToWorkflow();
  }

  const view = draft ?? committed;
  const dirty =
    draft != null && committed != null && !componentsEqual(draft, committed);
  // A provisional component always has something to save (it isn't persisted
  // yet).
  const hasUnsaved = canEdit && (dirty || isNew);

  // The upstream OpenTofu components whose outputs this component may
  // reference — those in an earlier stage — feeding the insert-a-reference
  // helper on the helm values/namespace fields.
  const upstreamOutputs = useMemo(
    () => upstreamTofuNames(nodeId, stages),
    [nodeId, stages],
  );

  // The reference set the helm fields' ${{ }} autocomplete may complete:
  // app-level variable names, the upstream OpenTofu component names (above), and
  // — keyed by component name — the output keys known from each one's latest
  // successful run. An upstream component with no known keys still appears (the
  // suggester offers its name and a "keys appear after a run" hint).
  const refContext = useMemo<RefContext>(() => {
    const outputKeysByName: Record<string, OutputKeyInfo[]> = {};
    for (const c of stages.flatMap((st) => st.components)) {
      if (!c.name || !upstreamOutputs.includes(c.name)) continue;
      const keys = componentOutputs[c.id];
      if (keys) outputKeysByName[c.name] = keys;
    }
    return {
      varsNames: appVariableNames,
      componentNames: upstreamOutputs,
      outputKeysByName,
    };
  }, [stages, upstreamOutputs, componentOutputs, appVariableNames]);

  // Rename check: other components reference this one's outputs by its
  // committed name, so renaming it breaks them until they're updated too. A
  // warning, never a block — the server accepts the save (the reference then
  // fails its own validation when that component saves, or the user fixes it).
  const renamedFrom =
    draft != null && committed != null && draft.name !== committed.name
      ? committed.name
      : "";
  const renameImpacted = useMemo(
    () =>
      renamedFrom
        ? componentsReferencing(
            renamedFrom,
            stages
              .flatMap((st) => st.components)
              .filter((c) => c.id !== nodeId),
          )
        : [],
    [renamedFrom, stages, nodeId],
  );

  return (
    <div className="mx-auto flex max-w-4xl flex-col">
      <button
        type="button"
        onClick={cancel}
        className="inline-flex w-fit items-center gap-1.5 text-sm text-neutral-400 hover:text-neutral-100"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to workflow
      </button>

      {loading ? (
        <p className="mt-6 text-sm text-neutral-400">Loading…</p>
      ) : error ? (
        <p className="mt-6 text-sm text-red-400">{error}</p>
      ) : !view ? (
        <div className="mt-6">
          <p className="text-sm text-neutral-300">
            That component isn’t in this workflow.
          </p>
          <button
            type="button"
            onClick={backToWorkflow}
            className="mt-2 text-sm font-medium text-neutral-300 hover:text-white"
          >
            Back to the workflow
          </button>
        </div>
      ) : (
        <>
          <div className="mt-3 flex items-start justify-between gap-3 pb-4">
            <div className="min-w-0">
              <p className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
                {view.type} component
                {placed && (
                  <span className="ml-1 normal-case tracking-normal">
                    · in stage {placed.index + 1}, {placed.stage.name}
                  </span>
                )}
                {isNew && <span className="ml-1 text-neutral-500">· new</span>}
              </p>
              <h1 className="mt-0.5 truncate text-xl font-bold tracking-tight">
                {view.name || "Edit component"}
              </h1>
            </div>
            {canEdit && (
              <button
                type="button"
                onClick={removeNode}
                className="inline-flex shrink-0 items-center gap-1.5 border border-red-500/40 px-3 py-1.5 text-sm text-red-300 hover:bg-red-500/10"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Delete component
              </button>
            )}
          </div>

          {!canEdit && (
            <p className="mb-4 border border-neutral-800 bg-neutral-800/50 px-3 py-2 text-sm text-neutral-300">
              You have view-only access to this workflow.
            </p>
          )}

          <div className="border border-neutral-800 bg-neutral-900 p-6">
            <ComponentFields
              component={view}
              onChange={setDraft}
              clusters={clusters}
              credentials={credentials}
              cloudCredentials={cloudCredentials}
              installations={installations}
              githubEnabled={githubEnabled}
              disabled={!canEdit}
              upstreamOutputs={upstreamOutputs}
              refContext={refContext}
            />
          </div>

          {renameImpacted.length > 0 && (
            <p className="mt-4 border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
              {renameImpacted.join(", ")}{" "}
              {renameImpacted.length === 1 ? "references" : "reference"} this
              component’s outputs as{" "}
              <code className="font-mono text-xs">
                {"${{ components." + renamedFrom + ".outputs.… }}"}
              </code>
              . Renaming it breaks those references — update them to the new
              name too.
            </p>
          )}

          {canEdit && (
            <div className="mt-4 flex items-center justify-end gap-3">
              <span className="mr-auto text-xs text-neutral-500">
                {hasUnsaved ? "Unsaved changes" : "Saved to workflow"}
              </span>
              <button
                type="button"
                onClick={cancel}
                className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={saveNode}
                disabled={!hasUnsaved}
                className="inline-flex items-center gap-1.5 bg-primary px-4 py-1.5 text-sm font-medium text-primary-fg hover:bg-primary-hover disabled:opacity-50"
              >
                <Save className="h-3.5 w-3.5" />
                Save component
              </button>
            </div>
          )}

          {/* Component variables. These override the app-level variables of the
              same name for this component's job, and write through their own
              endpoints — so only once the component is saved; a new one gets
              them from its page afterwards. */}
          {!isNew && (
            <div className="mt-6 border border-neutral-800 bg-neutral-900 p-4">
              <h2 className="text-[11px] font-medium uppercase tracking-wide text-neutral-500">
                Variables
              </h2>
              <p className="mb-3 mt-1 text-xs text-neutral-400">
                Passed to this component’s job as environment variables,
                overriding any app-level variable of the same name. Saved
                separately from the settings above. A sensitive value is sealed
                and never shown again.
              </p>
              <VariablesEditor
                scope={{ kind: "component", appId, componentId: nodeId }}
                canEdit={canEdit}
              />
            </div>
          )}

          {/* An OpenTofu component's recorded state: the resources it manages
              and its outputs, as of its last successful apply, plus — for an
              editor — the guarded state operations. Only for a saved
              component — a new one has no history. */}
          {!isNew && draft?.type === "terraform" && (
            <ComponentStatePanel
              appId={appId}
              componentId={nodeId}
              canEdit={canEdit}
            />
          )}
        </>
      )}
    </div>
  );
}
