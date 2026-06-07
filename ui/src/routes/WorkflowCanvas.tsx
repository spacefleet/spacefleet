import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowLeft,
  Boxes,
  FileCode,
  History,
  Package,
  Pencil,
  Play,
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

const nodeTypes = { component: BuilderNode, group: GroupNode };

// WorkflowCanvas is the interactive DAG canvas (index of
// /applications/:appId/workflow). It renders the draft's nodes/edges with React
// Flow, lets the user add nodes/groups and draw dependency edges, and persists
// the whole workflow with one PUT. Selecting a node shows a slim floating
// summary with an Edit button that opens the full-page node editor; there is no
// inline side panel. Run controls start a deploy/preview/uninstall.
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
    runError,
    running,
    forceRoll,
    setForceRoll,
    onNodesChange,
    onEdgesChange,
    onConnect,
    onNodeDragStop,
    addComponent,
    addGroup,
    deleteNode,
    save,
    startRun,
  } = useWorkflowDraft();

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = nodes.find((n) => n.id === selectedId) ?? null;
  const selectedName =
    selected?.type === "group"
      ? (selected.data as { name: string }).name
      : selected?.type === "component"
        ? (selected.data as { name: string }).name
        : null;

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
          <button
            type="button"
            onClick={() => navigate(`/applications/${appId}/runs`)}
            className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
          >
            <History className="h-3.5 w-3.5" />
            Run history
          </button>
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
                    label: "Group",
                    icon: <Boxes className="h-3.5 w-3.5" />,
                    onSelect: () => addGroup(),
                  },
                ]}
              />
              <button
                type="button"
                onClick={() => void save()}
                disabled={saving}
                className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
              >
                <Save className="h-3.5 w-3.5" />
                {saving ? "Saving…" : saved ? "Saved" : "Save"}
              </button>
              <span className="mx-1 h-5 w-px bg-neutral-200" />
              <button
                type="button"
                onClick={() => void startRun("preview")}
                disabled={running}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
              >
                Preview
              </button>
              <button
                type="button"
                onClick={() => void startRun("uninstall")}
                disabled={running}
                className="inline-flex items-center gap-1.5 border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Uninstall
              </button>
              <label
                title="Run the deploy's Helm step as a forced upgrade so pods roll even when the rendered manifests are unchanged"
                className="inline-flex items-center gap-1.5 text-sm text-neutral-600"
              >
                <input
                  type="checkbox"
                  checked={forceRoll}
                  onChange={(e) => setForceRoll(e.target.checked)}
                  className="h-3.5 w-3.5 accent-black"
                />
                Force workload roll
              </label>
              <button
                type="button"
                onClick={() => void startRun("deploy")}
                disabled={running}
                className="inline-flex items-center gap-1.5 bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
              >
                <Play className="h-3.5 w-3.5" />
                Deploy
              </button>
            </>
          )}
        </div>
      </div>

      {saveError && <p className="pb-2 text-sm text-red-600">{saveError}</p>}
      {runError && <p className="pb-2 text-sm text-red-600">{runError}</p>}

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
            onNodeClick={(_, n) => setSelectedId(n.id)}
            onNodeDragStop={(_, n: Node) => onNodeDragStop(n)}
            onPaneClick={() => setSelectedId(null)}
            nodesConnectable={canEdit}
            proOptions={{ hideAttribution: true }}
            fitView
          >
            <Background />
            <Controls />
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
                        (selected.data as { type: "helm" | "manifest" }).type
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
                    onClick={() => {
                      deleteNode(selected.id);
                      setSelectedId(null);
                    }}
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
