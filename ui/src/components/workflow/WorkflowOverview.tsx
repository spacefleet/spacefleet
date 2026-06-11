import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { ReactFlow, Background, type Edge, type Node } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { api } from "../../api/client";
import { useOrg } from "../../contexts/OrgContext";
import type { components } from "../../api/schema";
import { BuilderNode, GroupNode } from "./nodes";

type Workflow = components["schemas"]["Workflow"];

const nodeTypes = { component: BuilderNode, group: GroupNode };

// Default group size, mirroring the canvas's fallback for groups saved without
// explicit bounds.
const DEFAULT_GROUP_SIZE = { w: 320, h: 220 };

// WorkflowOverview is the read-only at-a-glance DAG shown on the application
// detail page: the saved workflow's components and groups laid out from their
// persisted canvas positions, with dependency edges, but with all editing
// interactions off. It answers "what's involved in a deploy" without opening
// the workflow editor; clicking a node jumps there.
export function WorkflowOverview({ appId }: { appId: string }) {
  const { currentOrg } = useOrg();
  const navigate = useNavigate();

  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    void (async () => {
      const { data, error } = await api.GET("/api/applications/{id}/workflow", {
        params: { path: { id: appId } },
      });
      if (cancelled) return;
      if (error || !data) {
        setError(error?.message ?? "Could not load the workflow");
      } else {
        setWorkflow(data);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [appId, currentOrg?.id]);

  // The same node/edge construction the editor's draft load does, minus all the
  // editable data: groups first (so they render behind their children), saved
  // positions with the editor's fallback grid, and one edge per dependency.
  const { nodes, edges } = useMemo(() => {
    const comps = workflow?.components ?? [];
    const groups = workflow?.groups ?? [];
    const groupIds = new Set(groups.map((g) => g.id));

    const groupNodes: Node[] = groups.map((g, i) => ({
      id: g.id,
      type: "group",
      position: {
        x: typeof g.position?.x === "number" ? g.position.x : 60 + i * 360,
        y: typeof g.position?.y === "number" ? g.position.y : 60,
      },
      width: typeof g.size?.w === "number" ? g.size.w : DEFAULT_GROUP_SIZE.w,
      height: typeof g.size?.h === "number" ? g.size.h : DEFAULT_GROUP_SIZE.h,
      data: { name: g.name },
    }));

    const componentNodes: Node[] = comps.map((c, i) => {
      const parentId =
        c.group_id && groupIds.has(c.group_id) ? c.group_id : undefined;
      return {
        id: c.id,
        type: "component",
        position: {
          x: typeof c.position?.x === "number" ? c.position.x : 80 + (i % 4) * 240,
          y:
            typeof c.position?.y === "number"
              ? c.position.y
              : 80 + Math.floor(i / 4) * 160,
        },
        ...(parentId ? { parentId } : {}),
        data: {
          name: c.name,
          type: c.type,
          continueOnFailure: c.continue_on_failure,
        },
      };
    });

    const flowEdges: Edge[] = [];
    for (const c of comps) {
      for (const dep of c.depends_on ?? []) {
        flowEdges.push({ id: `${dep}->${c.id}`, source: dep, target: c.id });
      }
    }
    for (const g of groups) {
      for (const dep of g.depends_on ?? []) {
        flowEdges.push({ id: `${dep}->${g.id}`, source: dep, target: g.id });
      }
    }

    return { nodes: [...groupNodes, ...componentNodes], edges: flowEdges };
  }, [workflow]);

  if (loading) {
    return <p className="px-4 py-6 text-sm text-neutral-500">Loading…</p>;
  }
  if (error) {
    return <p className="px-4 py-6 text-sm text-red-600">{error}</p>;
  }
  if (nodes.length === 0) {
    return (
      <p className="px-4 py-6 text-sm text-neutral-500">
        No components yet. Open the workflow editor to build the deploy
        workflow.
      </p>
    );
  }

  return (
    <div className="h-72 w-full">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        panOnDrag={false}
        zoomOnScroll={false}
        zoomOnPinch={false}
        zoomOnDoubleClick={false}
        preventScrolling={false}
        proOptions={{ hideAttribution: true }}
        onNodeClick={() => navigate(`/applications/${appId}/workflow`)}
        fitView
      >
        <Background />
      </ReactFlow>
    </div>
  );
}
