import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  Panel,
  useReactFlow,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowLeft,
  Boxes,
  FileCode,
  Layers,
  Package,
  Pencil,
  Plus,
  Save,
  Trash2,
} from "lucide-react";
import { useWorkflowDraft } from "../contexts/WorkflowDraftContext";
import {
  BuilderNode,
  GroupNode,
  TypeBadge,
} from "../components/workflow/nodes";
import { Dropdown } from "../components/Dropdown";
import type { components } from "../api/schema";

type ComponentType = components["schemas"]["ComponentType"];

const nodeTypes = { component: BuilderNode, group: GroupNode };

// WorkflowCanvas is the interactive DAG canvas (index of
// /applications/:appId/workflow). It renders the draft's nodes/edges with React
// Flow, lets the user add nodes/groups and draw dependency edges, and persists
// the whole workflow with one PUT. Selecting a node shows a slim floating
// summary with an Edit button that opens the full-page node editor; there is no
// inline side panel. This page is purely for building the workflow — runs are
// started (and their history viewed) from the application detail page.
export function WorkflowCanvas() {
  const { appId = "" } = useParams();
  const navigate = useNavigate();
  const {
    canEdit,
    nodes,
    edges,
    loading,
    error,
    saveError,
    saving,
    saved,
    focus,
    onNodesChange,
    onEdgesChange,
    onConnect,
    onNodeDrag,
    onNodeDragStop,
    addComponent,
    addGroup,
    groupSelection,
    deleteNode,
    save,
  } = useWorkflowDraft();

  const { fitView } = useReactFlow();

  // Track the current selection (React Flow drives it; shift-click adds to it).
  // The slim summary shows for a single selected node; the "Group" action appears
  // once two or more component nodes are selected.
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const onSelectionChange = useCallback(
    ({ nodes: sel }: { nodes: Node[] }) =>
      setSelectedIds(sel.map((n) => n.id)),
    [],
  );

  const selectedComponentIds = selectedIds.filter(
    (id) => nodes.find((n) => n.id === id)?.type === "component",
  );
  const selected =
    selectedIds.length === 1
      ? (nodes.find((n) => n.id === selectedIds[0]) ?? null)
      : null;
  const selectedName =
    selected?.type === "group"
      ? (selected.data as { name: string }).name
      : selected?.type === "component"
        ? (selected.data as { name: string }).name
        : null;

  // When the draft asks to frame nodes (after add/group), fit them into view so
  // it's obvious something happened — a freshly added node off-screen otherwise
  // gives no feedback. Runs after React Flow has the new nodes in its store
  // (child effects fire before this parent effect).
  useEffect(() => {
    if (!focus) return;
    void fitView({
      nodes: focus.ids.map((id) => ({ id })),
      duration: 500,
      padding: 0.4,
      maxZoom: 1.2,
    });
  }, [focus, fitView]);

  return (
    <div className="flex h-[calc(100vh-7rem)] flex-col">
      <div className="flex items-center justify-between gap-3 pb-3">
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
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          {canEdit && (
            <>
              <Dropdown
                trigger={
                  <>
                    <Plus className="h-3.5 w-3.5" />
                    Add
                  </>
                }
                items={[
                  {
                    label: "Helm",
                    icon: <Package className="h-3.5 w-3.5" />,
                    onSelect: () => addComponent("helm"),
                  },
                  {
                    label: "Manifest",
                    icon: <FileCode className="h-3.5 w-3.5" />,
                    onSelect: () => addComponent("manifest"),
                  },
                  {
                    label: "OpenTofu",
                    icon: <Layers className="h-3.5 w-3.5" />,
                    onSelect: () => addComponent("terraform"),
                  },
                  {
                    label: "Group",
                    icon: <Boxes className="h-3.5 w-3.5" />,
                    onSelect: () => addGroup(),
                  },
                ]}
              />
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
            </>
          )}
        </div>
      </div>

      {saveError && <p className="pb-2 text-sm text-red-600">{saveError}</p>}

      {loading ? (
        <p className="text-sm text-neutral-500">Loading…</p>
      ) : error ? (
        <p className="text-sm text-red-600">{error}</p>
      ) : (
        <div className="relative min-h-0 flex-1 border border-neutral-200">
          {nodes.length === 0 && (
            <p className="absolute left-1/2 top-1/2 z-10 -translate-x-1/2 -translate-y-1/2 text-sm text-neutral-400">
              No components yet. Use “+ Add” to begin.
            </p>
          )}
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onSelectionChange={onSelectionChange}
            onNodeDrag={(_, n: Node) => onNodeDrag(n)}
            onNodeDragStop={(_, n: Node) => onNodeDragStop(n)}
            multiSelectionKeyCode="Shift"
            selectionKeyCode="Shift"
            nodesConnectable={canEdit}
            proOptions={{ hideAttribution: true }}
            fitView
          >
            <Background />
            <Controls />
            {canEdit && selectedComponentIds.length >= 2 && (
              <Panel position="top-center">
                <button
                  type="button"
                  onClick={() => groupSelection(selectedComponentIds)}
                  className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white shadow-md hover:bg-neutral-800"
                >
                  <Boxes className="h-3.5 w-3.5" />
                  Group {selectedComponentIds.length} components
                </button>
              </Panel>
            )}
          </ReactFlow>

          {/* Slim floating summary for the selected node, replacing the old
              side panel: name + type badge, with Edit (full-page editor) and
              Delete actions. Group nodes show a group label instead of a type. */}
          {selected && (
            <div className="absolute right-3 top-3 z-10 w-72 border border-neutral-300 bg-white shadow-md">
              <div className="flex items-start justify-between gap-2 border-b border-neutral-200 px-3 py-2">
                <span className="truncate text-sm font-medium text-neutral-900">
                  {selectedName || "(unnamed)"}
                </span>
                {selected.type === "group" ? (
                  <span className="inline-flex shrink-0 items-center gap-1 border border-neutral-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-neutral-500">
                    <Boxes className="h-3 w-3" />
                    group
                  </span>
                ) : (
                  <span className="shrink-0">
                    <TypeBadge
                      type={
                        (selected.data as { type: ComponentType }).type
                      }
                    />
                  </span>
                )}
              </div>
              <div className="flex items-center justify-between gap-2 px-3 py-2">
                {selected.type === "component" && canEdit ? (
                  <button
                    type="button"
                    onClick={() => navigate(`nodes/${selected.id}`)}
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-2.5 py-1 text-sm text-neutral-700 hover:bg-neutral-50"
                  >
                    <Pencil className="h-3.5 w-3.5" />
                    Edit
                  </button>
                ) : selected.type === "component" ? (
                  <button
                    type="button"
                    onClick={() => navigate(`nodes/${selected.id}`)}
                    className="inline-flex items-center gap-1.5 border border-neutral-300 px-2.5 py-1 text-sm text-neutral-700 hover:bg-neutral-50"
                  >
                    View
                  </button>
                ) : (
                  <span className="text-xs text-neutral-500">
                    Drag components inside to group them.
                  </span>
                )}
                {canEdit && (
                  <button
                    type="button"
                    onClick={() => deleteNode(selected.id)}
                    className="inline-flex items-center gap-1.5 border border-red-300 px-2.5 py-1 text-sm text-red-700 hover:bg-red-50"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                    Delete
                  </button>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
