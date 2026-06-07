import { Handle, Position, type NodeProps } from "@xyflow/react";
import { Package, FileCode, RefreshCw, Boxes } from "lucide-react";
import type { components } from "../../api/schema";
import { ComponentStatusIcon } from "./status";
import { componentStatusClasses } from "./statusClasses";

type ComponentType = components["schemas"]["ComponentType"];
type ComponentRunStatus = components["schemas"]["ComponentRunStatus"];

// TypeBadge labels a node with its component type (helm/manifest) — sharp
// corners, neutral palette, a small leading glyph.
export function TypeBadge({ type }: { type: ComponentType }) {
  const Icon = type === "helm" ? Package : FileCode;
  return (
    <span className="inline-flex items-center gap-1 border border-neutral-300 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-neutral-500">
      <Icon className="h-3 w-3" />
      {type}
    </span>
  );
}

// BuilderNodeData is the payload the WorkflowBuilder attaches to each canvas
// node; the node component renders from it. `selected` is React Flow's own.
export interface BuilderNodeData extends Record<string, unknown> {
  name: string;
  type: ComponentType;
  continueOnFailure: boolean;
}

// BuilderNode is the editable canvas node: name + type badge + a
// continue-on-failure indicator, with connectable handles so edges can be drawn
// (target on top = "depends on", source on bottom). Sharp corners by brand.
export function BuilderNode({ data, selected }: NodeProps) {
  const d = data as BuilderNodeData;
  return (
    <div
      className={`min-w-[11rem] border bg-white px-3 py-2 shadow-sm ${
        selected ? "border-black ring-1 ring-black" : "border-neutral-300"
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-neutral-500" />
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium text-neutral-900">
          {d.name || "(unnamed)"}
        </span>
        {d.continueOnFailure && (
          <span title="Continue on failure: a failure here won't fail the run">
            <RefreshCw className="h-3 w-3 shrink-0 text-amber-600" />
          </span>
        )}
      </div>
      <div className="mt-1.5">
        <TypeBadge type={d.type} />
      </div>
      <Handle type="source" position={Position.Bottom} className="!bg-neutral-500" />
    </div>
  );
}

// GroupNodeData is the payload attached to a "group" container node: just the
// group's display name. Members are real child nodes rendered inside it (via
// React Flow parentId), so the group node itself only draws the labeled box.
export interface GroupNodeData extends Record<string, unknown> {
  name: string;
}

// GroupNode is the explicit parallel-group container: a labeled rectangular box
// (sharp corners, neutral border, faint fill) that member component nodes render
// inside. It carries source+target handles so an edge can be drawn to/from the
// whole group ("the group depends on …" / "… depends on the group"). Rendered
// behind its children by ordering group nodes first in the nodes array. Sized by
// React Flow from the node's width/height.
export function GroupNode({ data, selected }: NodeProps) {
  const d = data as GroupNodeData;
  return (
    <div
      className={`h-full w-full border bg-neutral-50/70 ${
        selected ? "border-black ring-1 ring-black" : "border-neutral-300"
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-neutral-500" />
      <div className="border-b border-neutral-200 bg-neutral-100/80 px-2 py-1">
        <span className="inline-flex items-center gap-1 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
          <Boxes className="h-3 w-3" />
          {d.name || "group"}
        </span>
      </div>
      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-neutral-500"
      />
    </div>
  );
}

// RunNodeData is what the WorkflowRunView attaches to each node: the snapshot's
// name/type plus the live ComponentRun status (and the component-run id used to
// fetch its detail on click).
export interface RunNodeData extends Record<string, unknown> {
  name: string;
  type: ComponentType;
  status: ComponentRunStatus;
}

// RunNode is the read-only run node, colored by status. Handles are present but
// not connectable (the run DAG is fixed). A failed/skipped node is visually
// muted; skipped is struck through.
export function RunNode({ data, selected }: NodeProps) {
  const d = data as RunNodeData;
  return (
    <div
      className={`min-w-[11rem] border px-3 py-2 shadow-sm ${componentStatusClasses(
        d.status,
      )} ${selected ? "ring-1 ring-black" : ""}`}
    >
      <Handle
        type="target"
        position={Position.Top}
        isConnectable={false}
        className="!bg-neutral-400"
      />
      <div className="flex items-center justify-between gap-2">
        <span
          className={`truncate text-sm font-medium text-neutral-900 ${
            d.status === "skipped" ? "line-through" : ""
          }`}
        >
          {d.name || "(unnamed)"}
        </span>
        <ComponentStatusIcon status={d.status} />
      </div>
      <div className="mt-1.5 flex items-center justify-between gap-2">
        <TypeBadge type={d.type} />
        <span className="text-[10px] uppercase tracking-wide text-neutral-500">
          {d.status}
        </span>
      </div>
      <Handle
        type="source"
        position={Position.Bottom}
        isConnectable={false}
        className="!bg-neutral-400"
      />
    </div>
  );
}
