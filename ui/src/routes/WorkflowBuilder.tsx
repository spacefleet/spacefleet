import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  FileCode,
  Layers,
  MoreVertical,
  Package,
  Plus,
  Save,
} from "lucide-react";
import {
  useWorkflowDraft,
  type DraftStage,
} from "../contexts/WorkflowDraftContext";
import { Dropdown } from "../components/Dropdown";
import {
  ComponentCardContent,
  StageConnector,
} from "../components/workflow/StageColumns";
import { componentSummary } from "../components/workflow/stageView";
import type { EditableComponent } from "../components/workflow/ComponentFields";

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
// components to a stage, and move components within or between stages (drag
// and drop, or the card's menu). Clicking a component opens the full-page
// editor. Every change auto-saves the whole workflow with one PUT. Runs are
// started (and their history viewed) from the application page, not here.
export function WorkflowBuilder() {
  const { appId = "" } = useParams();
  const navigate = useNavigate();
  const {
    canEdit,
    stages,
    clusters,
    loading,
    error,
    saveError,
    varFlushError,
    saving,
    saved,
    addStage,
    renameStage,
    deleteStage,
    moveStage,
    addComponent,
    moveComponent,
    deleteComponent,
    isProvisional,
    discardNewNode,
    save,
  } = useWorkflowDraft();

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

  const removeComponent = useCallback(
    (id: string) => {
      if (isProvisional(id)) discardNewNode(id);
      else deleteComponent(id);
    },
    [isProvisional, discardNewNode, deleteComponent],
  );

  return (
    <div>
      <div className="flex flex-wrap items-start justify-between gap-3 pb-3">
        <div className="min-w-0">
          <button
            type="button"
            onClick={() => navigate(`/applications/${appId}`)}
            className="inline-flex items-center gap-1.5 text-sm text-neutral-500 hover:text-neutral-900"
          >
            <ArrowLeft className="h-4 w-4" />
            Back to application
          </button>
          <h1 className="mt-1 text-xl font-bold tracking-tight">Workflow</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Stages run left to right, each once the one before it has
            finished. The components in a stage run in parallel.
          </p>
        </div>
        {canEdit && (
          <div className="flex flex-wrap items-center justify-end gap-2">
            <span className="text-xs text-neutral-400">
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
              className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
            >
              <Save className="h-3.5 w-3.5" />
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        )}
      </div>

      {saveError && <p className="pb-2 text-sm text-red-600">{saveError}</p>}
      {varFlushError && (
        <p className="pb-2 text-sm text-amber-700">{varFlushError}</p>
      )}

      {loading ? (
        <p className="text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="text-sm text-red-600">{error}</p>
      ) : (
        <div className="overflow-x-auto border border-neutral-200 bg-white">
          <div className="flex w-max min-w-full items-start p-4">
            {stages.length === 0 && !canEdit && (
              <p className="text-sm text-neutral-500">No components yet.</p>
            )}
            {stages.map((stage, i) => (
              <div key={stage.id} className="flex items-start">
                {i > 0 && <StageConnector />}
                <StageColumn
                  stage={stage}
                  index={i}
                  stageCount={stages.length}
                  prevStage={stages[i - 1]}
                  nextStage={stages[i + 1]}
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
                  onOpen={(id) => navigate(`nodes/${id}`)}
                  onMoveComponent={moveComponent}
                  onRemoveComponent={removeComponent}
                />
              </div>
            ))}
            {canEdit && (
              <div className="flex items-start">
                {stages.length > 0 && <StageConnector />}
                <button
                  type="button"
                  onClick={addStage}
                  className="flex h-24 w-48 shrink-0 items-center justify-center gap-1.5 border border-dashed border-neutral-300 text-sm text-neutral-500 hover:border-neutral-500 hover:text-neutral-900"
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
  prevStage,
  nextStage,
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
  onMoveComponent,
  onRemoveComponent,
}: {
  stage: DraftStage;
  index: number;
  stageCount: number;
  prevStage?: DraftStage;
  nextStage?: DraftStage;
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
  onMoveComponent: (id: string, toStageId: string, toIndex: number) => void;
  onRemoveComponent: (id: string) => void;
}) {
  const [confirmingDelete, setConfirmingDelete] = useState(false);
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
      className="flex w-72 shrink-0 flex-col border border-neutral-200 bg-neutral-50"
    >
      <header className="flex items-start justify-between gap-2 border-b border-neutral-200 px-3 py-2">
        <div className="min-w-0 flex-1">
          <p className="text-[10px] font-medium uppercase tracking-wide text-neutral-400">
            Stage {index + 1}
          </p>
          {canEdit ? (
            <StageNameInput name={stage.name} onCommit={onRename} />
          ) : (
            <h2 className="truncate text-sm font-semibold text-neutral-900">
              {stage.name}
            </h2>
          )}
        </div>
        {canEdit && (
          <Dropdown
            label={`Stage ${stage.name} actions`}
            triggerClassName="p-1 text-neutral-400 hover:text-neutral-900"
            trigger={<MoreVertical className="h-4 w-4" />}
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
              {
                label: "Delete stage",
                danger: true,
                onSelect: () =>
                  count > 0 ? setConfirmingDelete(true) : onDelete(),
              },
            ]}
          />
        )}
      </header>

      {confirmingDelete && (
        <div className="border-b border-red-200 bg-red-50 px-3 py-2">
          <p className="text-xs text-red-800">
            Delete “{stage.name}” and its {count}{" "}
            {count === 1 ? "component" : "components"}?
          </p>
          <div className="mt-2 flex gap-2">
            <button
              type="button"
              onClick={() => {
                setConfirmingDelete(false);
                onDelete();
              }}
              className="bg-red-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-red-700"
            >
              Delete stage
            </button>
            <button
              type="button"
              onClick={() => setConfirmingDelete(false)}
              className="border border-neutral-300 bg-white px-2.5 py-1 text-xs text-neutral-700 hover:bg-neutral-50"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

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
          <p className="px-1 py-2 text-xs text-neutral-400">
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
              canMoveUp={i > 0}
              canMoveDown={i < count - 1}
              prevStage={prevStage}
              nextStage={nextStage}
              onOpen={() => onOpen(c.id)}
              onDragStart={() => onDragStart(c.id)}
              onDragEnd={onDragEnd}
              onDragOverHalf={(after) => {
                const at = after ? i + 1 : i;
                if (indicatorAt !== at) onDragTarget({ stageId: stage.id, index: at });
              }}
              onMoveUp={() => onMoveComponent(c.id, stage.id, i - 1)}
              onMoveDown={() => onMoveComponent(c.id, stage.id, i + 1)}
              onMoveToStage={(st) =>
                onMoveComponent(c.id, st.id, st.components.length)
              }
              onRemove={() => onRemoveComponent(c.id)}
            />
          </div>
        ))}
        {indicatorAt === count && count > 0 && <DropIndicator />}
        {indicatorAt === 0 && count === 0 && <DropIndicator />}
      </div>

      {canEdit && (
        <div className="border-t border-neutral-200 p-2">
          <Dropdown
            align="left"
            label={`Add a component to ${stage.name}`}
            triggerClassName="inline-flex items-center gap-1.5 px-1 py-1 text-sm text-neutral-600 hover:text-neutral-900"
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
  return <div aria-hidden="true" className="mb-2 h-0.5 bg-black" />;
}

// BuilderCard is one component in a stage: the card body opens the editor, the
// whole card is draggable for an editor, and the menu offers the same moves
// without dragging (and a delete).
function BuilderCard({
  component,
  summary,
  isNew,
  canEdit,
  dragging,
  dragActive,
  canMoveUp,
  canMoveDown,
  prevStage,
  nextStage,
  onOpen,
  onDragStart,
  onDragEnd,
  onDragOverHalf,
  onMoveUp,
  onMoveDown,
  onMoveToStage,
  onRemove,
}: {
  component: EditableComponent;
  summary: string;
  isNew: boolean;
  canEdit: boolean;
  dragging: boolean;
  // Whether one of the builder's cards is being dragged; drags from outside
  // (a file, text) are ignored.
  dragActive: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  prevStage?: DraftStage;
  nextStage?: DraftStage;
  onOpen: () => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDragOverHalf: (after: boolean) => void;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onMoveToStage: (stage: DraftStage) => void;
  onRemove: () => void;
}) {
  return (
    <div
      draggable={canEdit}
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
      className={`flex items-start border border-neutral-300 bg-white ${
        canEdit ? "cursor-grab" : ""
      } ${dragging ? "opacity-40" : ""}`}
    >
      {/* Not a <button>: Firefox won't start a drag on one, and the body is
          most of the card. It's still focusable and opens on Enter/Space. */}
      <div
        role="button"
        tabIndex={0}
        onClick={onOpen}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onOpen();
          }
        }}
        className="min-w-0 flex-1 px-3 py-2 text-left hover:bg-neutral-50 focus:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-black"
      >
        <ComponentCardContent
          component={component}
          summary={summary}
          isNew={isNew}
        />
      </div>
      {canEdit && (
        <div className="pr-1 pt-1.5">
          <Dropdown
            label={`${component.name} actions`}
            triggerClassName="p-1 text-neutral-400 hover:text-neutral-900"
            trigger={<MoreVertical className="h-4 w-4" />}
            items={[
              { label: "Move up", disabled: !canMoveUp, onSelect: onMoveUp },
              {
                label: "Move down",
                disabled: !canMoveDown,
                onSelect: onMoveDown,
              },
              {
                label: "Move to previous stage",
                disabled: !prevStage,
                onSelect: () => prevStage && onMoveToStage(prevStage),
              },
              {
                label: "Move to next stage",
                disabled: !nextStage,
                onSelect: () => nextStage && onMoveToStage(nextStage),
              },
              { label: "Delete component", danger: true, onSelect: onRemove },
            ]}
          />
        </div>
      )}
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
      className="-ml-1 w-full border border-transparent bg-transparent px-1 text-sm font-semibold text-neutral-900 hover:border-neutral-300 focus:border-black focus:bg-white focus:outline-none"
    />
  );
}
