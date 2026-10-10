import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  FileCode,
  Layers,
  Package,
  Plus,
  Save,
} from "lucide-react";
import {
  useWorkflowDraft,
  type DraftStage,
} from "../contexts/WorkflowDraftContext";
import { Dropdown } from "../components/Dropdown";
import { ActionsMenu } from "../components/ActionsMenu";
import {
  ComponentCardContent,
  StageConnector,
} from "../components/workflow/StageColumns";
import { componentSummary } from "../components/workflow/stageView";
import type { EditableComponent } from "../components/workflow/ComponentFields";
import { useDocumentTitle } from "../lib/useDocumentTitle";

// Where a dragged component would land: a stage and an index into that
// stage's components as currently displayed (the dragged one included).
interface DropTarget {
  stageId: string;
  index: number;
}

// WorkflowBuilder is the stage builder (index of /applications/:appId/workflow).
// A workflow is an ordered list of stages, shown as columns left to right; the
// components in a stage run in parallel, and each stage starts once the one
// before it has finished. Editors rename, reorder, add, and delete stages, add
// components to a stage, and drag components within or between stages.
// Clicking a component opens its page, where it's managed and deleted. Every
// change auto-saves the whole workflow with one PUT. Runs are started (and
// their history viewed) from the application page, not here.
export function WorkflowBuilder() {
  const { appId = "" } = useParams();
  const navigate = useNavigate();
  const {
    appName,
    canEdit,
    stages,
    clusters,
    loading,
    error,
    saveError,
    backendChange,
    confirmBackendChange,
    saving,
    saved,
    addStage,
    renameStage,
    deleteStage,
    moveStage,
    addComponent,
    moveComponent,
    isProvisional,
    save,
  } = useWorkflowDraft();
  useDocumentTitle("Workflow", appName);

  const clusterName = useCallback(
    (id: string) => clusters.find((c) => c.id === id)?.name,
    [clusters],
  );

  // Drag-and-drop state lives here (not in the browser's dataTransfer) so the
  // drop indicator can render while dragging and so it works the same in every
  // browser.
  const [dragId, setDragId] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<DropTarget | null>(null);

  const endDrag = useCallback(() => {
    setDragId(null);
    setDropTarget(null);
  }, []);

  // drop applies the pending move. The target index counts the dragged card
  // where it currently sits, so a move later within its own stage shifts by one
  // once it's taken out. Dropping a card back where it was changes nothing (and
  // so doesn't mark the draft dirty).
  const drop = useCallback(() => {
    if (!dragId || !dropTarget) return endDrag();
    const fromStage = stages.find((st) =>
      st.components.some((c) => c.id === dragId),
    );
    if (!fromStage) return endDrag();
    const fromIndex = fromStage.components.findIndex((c) => c.id === dragId);
    let to = dropTarget.index;
    if (fromStage.id === dropTarget.stageId && fromIndex < to) to -= 1;
    if (fromStage.id !== dropTarget.stageId || to !== fromIndex) {
      moveComponent(dragId, dropTarget.stageId, to);
    }
    endDrag();
  }, [dragId, dropTarget, stages, moveComponent, endDrag]);

  return (
    <div>
      <div className="flex flex-wrap items-start justify-between gap-3 pb-3">
        <div className="min-w-0">
          <button
            type="button"
            onClick={() => navigate(`/applications/${appId}`)}
            className="inline-flex items-center gap-1.5 text-sm text-neutral-400 hover:text-neutral-100"
          >
            <ArrowLeft className="h-4 w-4" />
            Back to application
          </button>
          <h1 className="mt-1 text-xl font-bold tracking-tight">Workflow</h1>
          <p className="mt-1 text-sm text-neutral-300">
            Stages run left to right, each once the one before it has
            finished. The components in a stage run in parallel.
          </p>
        </div>
        {canEdit && (
          <div className="flex flex-wrap items-center justify-end gap-2">
            <span className="text-xs text-neutral-500">
              {saving
                ? "Saving…"
                : saved
                  ? "All changes saved"
                  : "Unsaved changes"}
            </span>
            <button
              type="button"
              onClick={() => void save()}
              disabled={saving}
              title="Changes save automatically — click to save now"
              className="inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800 disabled:opacity-50"
            >
              <Save className="h-3.5 w-3.5" />
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        )}
      </div>

      {saveError && <p className="pb-2 text-sm text-red-400">{saveError}</p>}
      {backendChange && (
        <div
          role="alert"
          className="mb-3 border border-amber-500/30 bg-amber-500/10 p-4"
        >
          <p className="text-sm font-medium text-amber-200">
            Not saved — this moves existing OpenTofu state
          </p>
          <p className="mt-1 text-sm text-amber-300">
            A component you changed still manages resources in its current
            state backend. The new backend starts empty, so the next run would
            plan to create everything again. Destroy the resources first (or
            switch the backend back), unless you have already moved the state
            yourself.
          </p>
          <p className="mt-1 text-xs text-amber-300">{backendChange}</p>
          <button
            type="button"
            onClick={() => void confirmBackendChange()}
            disabled={saving}
            className="mt-3 border border-amber-400 bg-neutral-900 px-3 py-1.5 text-sm font-medium text-amber-200 hover:bg-amber-500/15 disabled:opacity-50"
          >
            Switch the backend anyway
          </button>
        </div>
      )}

      {loading ? (
        <p className="text-sm text-neutral-400">Loading…</p>
      ) : error ? (
        <p className="text-sm text-red-400">{error}</p>
      ) : (
        <div className="overflow-x-auto border border-neutral-800 bg-neutral-900">
          <div className="flex w-max min-w-full items-start p-4">
            {stages.length === 0 && !canEdit && (
              <p className="text-sm text-neutral-400">No components yet.</p>
            )}
            {stages.map((stage, i) => (
              <div key={stage.id} className="flex items-start">
                {i > 0 && <StageConnector />}
                <StageColumn
                  stage={stage}
                  index={i}
                  stageCount={stages.length}
                  canEdit={canEdit}
                  clusterName={clusterName}
                  isProvisional={isProvisional}
                  dragId={dragId}
                  dropTarget={dropTarget}
                  onDragStart={setDragId}
                  onDragEnd={endDrag}
                  onDragTarget={setDropTarget}
                  onDrop={drop}
                  onRename={(name) => renameStage(stage.id, name)}
                  onMove={(delta) => moveStage(stage.id, delta)}
                  onDelete={() => deleteStage(stage.id)}
                  onAdd={(type) => addComponent(stage.id, type)}
                  onOpen={(id) =>
                    navigate(isProvisional(id) ? `nodes/${id}/edit` : `nodes/${id}`)
                  }
                />
              </div>
            ))}
            {canEdit && (
              <div className="flex items-start">
                {stages.length > 0 && <StageConnector />}
                <button
                  type="button"
                  onClick={addStage}
                  className="flex h-24 w-48 shrink-0 items-center justify-center gap-1.5 border border-dashed border-neutral-700 text-sm text-neutral-400 hover:border-neutral-500 hover:text-neutral-100"
                >
                  <Plus className="h-4 w-4" />
                  Add stage
                </button>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// StageColumn is one editable stage: its name and menu, its components in
// order (each a drag source and drop target), and the add-component control.
function StageColumn({
  stage,
  index,
  stageCount,
  canEdit,
  clusterName,
  isProvisional,
  dragId,
  dropTarget,
  onDragStart,
  onDragEnd,
  onDragTarget,
  onDrop,
  onRename,
  onMove,
  onDelete,
  onAdd,
  onOpen,
}: {
  stage: DraftStage;
  index: number;
  stageCount: number;
  canEdit: boolean;
  clusterName: (id: string) => string | undefined;
  isProvisional: (id: string) => boolean;
  dragId: string | null;
  dropTarget: DropTarget | null;
  onDragStart: (id: string) => void;
  onDragEnd: () => void;
  onDragTarget: (t: DropTarget) => void;
  onDrop: () => void;
  onRename: (name: string) => void;
  onMove: (delta: -1 | 1) => void;
  onDelete: () => void;
  onAdd: (type: EditableComponent["type"]) => void;
  onOpen: (id: string) => void;
}) {
  const count = stage.components.length;
  const indicatorAt =
    dragId && dropTarget?.stageId === stage.id ? dropTarget.index : null;

  // Dragging over the column's empty space (below the cards) drops at the end;
  // the cards themselves claim the events over them.
  function onColumnDragOver(e: DragEvent) {
    if (!dragId) return;
    e.preventDefault();
    if (indicatorAt !== count) onDragTarget({ stageId: stage.id, index: count });
  }

  return (
    <section
      aria-label={`Stage ${stage.name}`}
      className="flex w-72 shrink-0 flex-col border border-neutral-800 bg-neutral-900/40"
    >
      <header className="flex items-start justify-between gap-2 bg-neutral-800/60 px-3 py-2">
        <div className="min-w-0 flex-1">
          <p className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">
            Stage {index + 1}
          </p>
          {canEdit ? (
            <StageNameInput name={stage.name} onCommit={onRename} />
          ) : (
            <h2 className="truncate text-sm font-semibold text-neutral-100">
              {stage.name}
            </h2>
          )}
        </div>
        {canEdit && (
          <ActionsMenu
            label={`Stage ${stage.name} actions`}
            items={[
              {
                label: "Move left",
                disabled: index === 0,
                onSelect: () => onMove(-1),
              },
              {
                label: "Move right",
                disabled: index === stageCount - 1,
                onSelect: () => onMove(1),
              },
              // Only an empty stage can go: each component's own Delete asks
              // what happens to what it deployed, which deleting the stage
              // would skip.
              {
                label: "Delete stage",
                danger: true,
                disabled: count > 0,
                hint: count > 0 ? "Delete or move its components first" : undefined,
                onSelect: onDelete,
              },
            ]}
          />
        )}
      </header>

      <div
        data-testid={`stage-drop-${stage.id}`}
        onDragOver={onColumnDragOver}
        onDrop={(e) => {
          e.preventDefault();
          onDrop();
        }}
        className="flex min-h-24 flex-1 flex-col gap-2 p-2"
      >
        {count === 0 && indicatorAt === null && (
          <p className="px-1 py-2 text-xs text-neutral-500">
            No components yet.
          </p>
        )}
        {stage.components.map((c, i) => (
          <div key={c.id}>
            {indicatorAt === i && <DropIndicator />}
            <BuilderCard
              component={c}
              summary={componentSummary(c, clusterName)}
              isNew={isProvisional(c.id)}
              canEdit={canEdit}
              dragging={dragId === c.id}
              dragActive={dragId != null}
              onOpen={() => onOpen(c.id)}
              onDragStart={() => onDragStart(c.id)}
              onDragEnd={onDragEnd}
              onDragOverHalf={(after) => {
                const at = after ? i + 1 : i;
                if (indicatorAt !== at) onDragTarget({ stageId: stage.id, index: at });
              }}
            />
          </div>
        ))}
        {indicatorAt === count && count > 0 && <DropIndicator />}
        {indicatorAt === 0 && count === 0 && <DropIndicator />}
      </div>

      {canEdit && (
        <div className="p-2">
          <Dropdown
            align="left"
            label={`Add a component to ${stage.name}`}
            triggerClassName="inline-flex items-center gap-1.5 px-1 py-1 text-sm text-neutral-300 hover:text-neutral-100"
            trigger={
              <>
                <Plus className="h-3.5 w-3.5" />
                Add component
              </>
            }
            items={[
              {
                label: "Helm",
                icon: <Package className="h-3.5 w-3.5" />,
                onSelect: () => onAdd("helm"),
              },
              {
                label: "Manifest",
                icon: <FileCode className="h-3.5 w-3.5" />,
                onSelect: () => onAdd("manifest"),
              },
              {
                label: "OpenTofu",
                icon: <Layers className="h-3.5 w-3.5" />,
                onSelect: () => onAdd("terraform"),
              },
            ]}
          />
        </div>
      )}
    </section>
  );
}

function DropIndicator() {
  return <div aria-hidden="true" className="mb-2 h-0.5 bg-white" />;
}

// BuilderCard is one component in a stage: clicking it opens the editor, and
// an editor drags it to reorder or move it to another stage. Not a <button>:
// Firefox won't start a drag on one. It's still focusable and opens on
// Enter/Space.
function BuilderCard({
  component,
  summary,
  isNew,
  canEdit,
  dragging,
  dragActive,
  onOpen,
  onDragStart,
  onDragEnd,
  onDragOverHalf,
}: {
  component: EditableComponent;
  summary: string;
  isNew: boolean;
  canEdit: boolean;
  dragging: boolean;
  // Whether one of the builder's cards is being dragged; drags from outside
  // (a file, text) are ignored.
  dragActive: boolean;
  onOpen: () => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDragOverHalf: (after: boolean) => void;
}) {
  return (
    <div
      role="button"
      tabIndex={0}
      draggable={canEdit}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen();
        }
      }}
      onDragStart={(e) => {
        // Firefox only starts a drag when some data is set.
        e.dataTransfer?.setData("text/plain", component.id);
        if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
        onDragStart();
      }}
      onDragEnd={onDragEnd}
      onDragOver={(e) => {
        if (!dragActive) return;
        e.preventDefault();
        e.stopPropagation();
        const rect = e.currentTarget.getBoundingClientRect();
        onDragOverHalf(e.clientY > rect.top + rect.height / 2);
      }}
      className={`border border-neutral-700 bg-neutral-900 px-3 py-2 text-left hover:bg-neutral-800 focus:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-white ${
        canEdit ? "cursor-grab" : "cursor-pointer"
      } ${dragging ? "opacity-40" : ""}`}
    >
      <ComponentCardContent
        component={component}
        summary={summary}
        isNew={isNew}
      />
    </div>
  );
}

// StageNameInput edits a stage's name in place. Edits stay local until the
// field is left (or Enter); an empty name reverts rather than saving a stage
// the server would reject, and Escape abandons the edit.
function StageNameInput({
  name,
  onCommit,
}: {
  name: string;
  onCommit: (name: string) => void;
}) {
  const [value, setValue] = useState(name);
  const [editing, setEditing] = useState(false);
  // Set by Escape so the blur that follows doesn't commit the abandoned text.
  const abandon = useRef(false);

  // Follow the draft (e.g. after a reload) unless the user is mid-edit.
  useEffect(() => {
    if (!editing) setValue(name);
  }, [name, editing]);

  function commit() {
    setEditing(false);
    if (abandon.current) {
      abandon.current = false;
      setValue(name);
      return;
    }
    const next = value.trim();
    if (next && next !== name) onCommit(next);
    else setValue(name);
  }

  return (
    <input
      aria-label="Stage name"
      value={value}
      maxLength={100}
      onFocus={() => setEditing(true)}
      onChange={(e) => setValue(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
        if (e.key === "Escape") {
          abandon.current = true;
          e.currentTarget.blur();
        }
      }}
      className="-ml-1 w-full border border-transparent bg-transparent px-1 text-sm font-semibold text-neutral-100 hover:border-neutral-700 focus:border-white focus:bg-neutral-900 focus:outline-none"
    />
  );
}
