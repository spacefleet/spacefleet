import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useNavigate, useParams } from "react-router";
import {
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge,
  type EdgeChange,
  type Node,
  type NodeChange,
} from "@xyflow/react";
import { api } from "../api/client";
import { useOrg } from "./OrgContext";
import type { components } from "../api/schema";
import { githubAppEnabled } from "../lib/appConfig";
import type { BuilderNodeData, GroupNodeData } from "../components/workflow/nodes";
import type { EditableComponent } from "../components/workflow/ComponentFields";

type Component = components["schemas"]["Component"];
type ComponentInput = components["schemas"]["ComponentInput"];
type ComponentGroup = components["schemas"]["ComponentGroup"];
type ComponentGroupInput = components["schemas"]["ComponentGroupInput"];
type ComponentType = components["schemas"]["ComponentType"];
type RunAction = components["schemas"]["RunAction"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];

// A component (type "component") node carries the editable component as its data;
// a group (type "group") node carries just the group name. Both share the canvas.
export type FlowNode = Node<
  (BuilderNodeData & { component: EditableComponent }) | GroupNodeData
>;

// Default size for a freshly added group container.
const DEFAULT_GROUP_SIZE = { w: 320, h: 220 };

function isGroupNode(n: FlowNode): boolean {
  return n.type === "group";
}

// toEditable strips position/depends_on/group_id (which live on the node + edges)
// off a loaded Component into the working shape the editor edits.
function toEditable(c: Component): EditableComponent {
  return {
    id: c.id,
    name: c.name,
    type: c.type,
    config: { ...c.config },
    continue_on_failure: c.continue_on_failure,
    target_cluster_id: c.target_cluster_id ?? null,
    target_namespace: c.target_namespace ?? "",
    chart_credential_id: c.chart_credential_id ?? null,
    github_installation_id: c.github_installation_id ?? null,
  };
}

interface WorkflowDraftValue {
  appId: string;
  canEdit: boolean;
  githubEnabled: boolean;

  nodes: FlowNode[];
  edges: Edge[];
  clusters: Cluster[];
  credentials: ChartCredential[];
  installations: GitHubInstallation[];

  loading: boolean;
  error: string | null;
  saveError: string | null;
  saving: boolean;
  saved: boolean;
  runError: string | null;
  running: boolean;
  forceRoll: boolean;
  setForceRoll: (v: boolean) => void;

  onNodesChange: (changes: NodeChange[]) => void;
  onEdgesChange: (changes: EdgeChange[]) => void;
  onConnect: (conn: Connection) => void;
  onNodeDragStop: (node: Node) => void;

  addComponent: (type: ComponentType) => void;
  addGroup: () => void;
  updateComponent: (next: EditableComponent) => void;
  deleteNode: (id: string) => void;
  getComponent: (id: string) => EditableComponent | null;

  save: () => Promise<void>;
  startRun: (action: RunAction) => Promise<void>;
}

const WorkflowDraftContext = createContext<WorkflowDraftValue | null>(null);

// useWorkflowDraft reads the in-memory workflow draft shared across the canvas
// and the full-page node editor. Throws if used outside the provider.
// eslint-disable-next-line react-refresh/only-export-components
export function useWorkflowDraft(): WorkflowDraftValue {
  const ctx = useContext(WorkflowDraftContext);
  if (!ctx)
    throw new Error("useWorkflowDraft must be used within a WorkflowDraftProvider");
  return ctx;
}

// WorkflowDraftProvider owns the whole workflow draft (nodes/edges/groups +
// reference data + save/run state) so unsaved edits survive navigating between
// the canvas (the DAG) and the full-page node editor (which live on nested
// routes under one layout). It loads the workflow on mount when the draft is
// empty so a deep link to the editor route works, and reloads when the org
// changes (mirroring the old builder's effect deps).
export function WorkflowDraftProvider({ children }: { children: ReactNode }) {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";
  const githubEnabled = githubAppEnabled();

  const [nodes, setNodes] = useState<FlowNode[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);

  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [credentials, setCredentials] = useState<ChartCredential[]>([]);
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [forceRoll, setForceRoll] = useState(false);

  // Load the workflow and lay out group + component nodes from their persisted
  // position. Group nodes come first in the array so they render behind their
  // children; component nodes whose group_id is set become React Flow children.
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const { data, error } = await api.GET("/api/applications/{id}/workflow", {
      params: { path: { id: appId } },
    });
    if (error || !data) {
      setError(error?.message ?? "Could not load this workflow");
      setLoading(false);
      return;
    }
    const comps = data.components;
    const groups: ComponentGroup[] = data.groups ?? [];

    const groupNodes: FlowNode[] = groups.map((g, i) => ({
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

    const groupIds = new Set(groups.map((g) => g.id));
    const componentNodes: FlowNode[] = comps.map((c, i) => {
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
        ...(parentId ? { parentId, extent: "parent" as const } : {}),
        data: {
          name: c.name,
          type: c.type,
          continueOnFailure: c.continue_on_failure,
          component: toEditable(c),
        },
      };
    });

    // An edge per dependency: dep → node. dep may reference a component or a
    // group, and the target may be a component or a group — both endpoints are
    // real nodes on the canvas, so the edge resolves either way.
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

    // Group nodes first so they render behind their children.
    setNodes([...groupNodes, ...componentNodes]);
    setEdges(flowEdges);
    setLoading(false);
  }, [appId]);

  // Load on mount and whenever the org changes. Re-running on org change mirrors
  // the original builder; it also resets any unsaved draft, which is correct
  // because the draft belongs to the previously-selected org.
  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Reference data for the editor's selects (same fetches as the app form).
  useEffect(() => {
    void (async () => {
      const { data } = await api.GET("/api/clusters");
      setClusters(data ?? []);
    })();
    void (async () => {
      const { data } = await api.GET("/api/chart-credentials");
      setCredentials(data ?? []);
    })();
    if (githubEnabled) {
      void (async () => {
        const { data } = await api.GET("/api/github/installations");
        setInstallations(data ?? []);
      })();
    }
  }, [githubEnabled, currentOrg?.id]);

  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setNodes((ns) => applyNodeChanges(changes, ns) as FlowNode[]);
    setSaved(false);
  }, []);
  const onEdgesChange = useCallback((changes: EdgeChange[]) => {
    setEdges((es) => applyEdgeChanges(changes, es));
    setSaved(false);
  }, []);
  const onConnect = useCallback((conn: Connection) => {
    if (!conn.source || !conn.target || conn.source === conn.target) return;
    setEdges((es) => addEdge({ id: `${conn.source}->${conn.target}`, ...conn }, es));
    setSaved(false);
  }, []);

  // On drag stop, decide group membership: if a component node's bounds land
  // inside a group node, adopt it (parentId + position relative to the parent);
  // if dragged clear of every group, detach it. Group nodes themselves never
  // become children.
  const onNodeDragStop = useCallback((dragged: Node) => {
    setNodes((ns) => {
      const node = ns.find((n) => n.id === dragged.id) as FlowNode | undefined;
      if (!node || isGroupNode(node)) return ns;

      // Absolute position of the dragged node (account for its current parent).
      const parent = node.parentId
        ? ns.find((n) => n.id === node.parentId)
        : undefined;
      const abs = {
        x: (parent?.position.x ?? 0) + node.position.x,
        y: (parent?.position.y ?? 0) + node.position.y,
      };

      // Find a group whose bounds contain the dragged node's top-left point.
      const target = ns.find((g) => {
        if (!isGroupNode(g)) return false;
        const w = g.width ?? DEFAULT_GROUP_SIZE.w;
        const h = g.height ?? DEFAULT_GROUP_SIZE.h;
        return (
          abs.x >= g.position.x &&
          abs.x <= g.position.x + w &&
          abs.y >= g.position.y &&
          abs.y <= g.position.y + h
        );
      });

      const nextParentId = target?.id;
      if (nextParentId === node.parentId) return ns; // no change

      setSaved(false);
      return ns.map((n) => {
        if (n.id !== node.id) return n;
        if (nextParentId) {
          const g = ns.find((x) => x.id === nextParentId)!;
          return {
            ...n,
            parentId: nextParentId,
            extent: "parent" as const,
            position: { x: abs.x - g.position.x, y: abs.y - g.position.y },
          };
        }
        // Detached: restore absolute position, drop parent linkage.
        const rest = { ...n, position: abs };
        delete rest.parentId;
        delete rest.extent;
        return rest;
      });
    });
  }, []);

  const addComponent = useCallback((type: ComponentType) => {
    const id = crypto.randomUUID();
    const editable: EditableComponent = {
      id,
      name: type === "helm" ? "helm release" : "manifest apply",
      type,
      config: type === "helm" ? { chart_source: "http_repo" } : {},
      continue_on_failure: false,
      target_cluster_id: null,
      target_namespace: "",
      chart_credential_id: null,
      github_installation_id: null,
    };
    setNodes((ns) => {
      // Tile new top-level nodes into a non-overlapping grid (same spacing as the
      // load-time fallback layout), rather than stacking them diagonally near the
      // center where they overlap and are hard to select. Children of a group are
      // positioned relative to their parent, so they don't count toward the slot.
      const slot = ns.filter((n) => n.type === "component" && !n.parentId).length;
      return [
      ...ns,
      {
        id,
        type: "component",
        position: { x: 80 + (slot % 4) * 240, y: 80 + Math.floor(slot / 4) * 160 },
        data: {
          name: editable.name,
          type: editable.type,
          continueOnFailure: false,
          component: editable,
        },
      },
      ];
    });
    setSaved(false);
  }, []);

  const addGroup = useCallback(() => {
    const id = crypto.randomUUID();
    setNodes((ns) => {
      const groupCount = ns.filter(isGroupNode).length;
      const groupNode: FlowNode = {
        id,
        type: "group",
        // Lay groups out in their own lower band, spaced by a full group width so
        // they don't overlap each other or the top-level component grid above.
        position: {
          x: 80 + groupCount * (DEFAULT_GROUP_SIZE.w + 48),
          y: 80 + 3 * 160 + 40,
        },
        width: DEFAULT_GROUP_SIZE.w,
        height: DEFAULT_GROUP_SIZE.h,
        data: { name: "group" },
      };
      // Keep group nodes first so they render behind children.
      return [groupNode, ...ns];
    });
    setSaved(false);
  }, []);

  const updateComponent = useCallback((next: EditableComponent) => {
    setNodes((ns) =>
      ns.map((n) =>
        n.id === next.id && n.type === "component"
          ? {
              ...n,
              data: {
                name: next.name,
                type: next.type,
                continueOnFailure: next.continue_on_failure,
                component: next,
              },
            }
          : n,
      ),
    );
    setSaved(false);
  }, []);

  const deleteNode = useCallback((id: string) => {
    setNodes((ns) => {
      // Detach any children of a deleted group so they aren't orphaned (React
      // Flow would drop a child whose parent is gone). Restore absolute position.
      const target = ns.find((n) => n.id === id);
      const isGroup = target?.type === "group";
      return ns
        .filter((n) => n.id !== id)
        .map((n) => {
          if (isGroup && n.parentId === id) {
            const abs = {
              x: (target?.position.x ?? 0) + n.position.x,
              y: (target?.position.y ?? 0) + n.position.y,
            };
            const rest = { ...n, position: abs };
            delete rest.parentId;
            delete rest.extent;
            return rest;
          }
          return n;
        });
    });
    setEdges((es) => es.filter((e) => e.source !== id && e.target !== id));
    setSaved(false);
  }, []);

  const getComponent = useCallback(
    (id: string): EditableComponent | null => {
      const n = nodes.find((x) => x.id === id);
      if (!n || n.type !== "component") return null;
      return (n.data as { component: EditableComponent }).component;
    },
    [nodes],
  );

  // Assemble the PUT payload from nodes + edges. For each edge source→target the
  // source id is contributed into the target's depends_on (the target may be a
  // component or a group). A component's group_id is its parentId when that
  // parent is a group node. Positions are stored as-is (child positions are
  // parent-relative, which load restores alongside parentId).
  const buildPayload = useCallback((): {
    components: ComponentInput[];
    groups: ComponentGroupInput[];
  } => {
    const groupIds = new Set(nodes.filter(isGroupNode).map((n) => n.id));

    // depends_on per target id, built from inbound edges.
    const dependsByTarget = new Map<string, string[]>();
    for (const e of edges) {
      const arr = dependsByTarget.get(e.target) ?? [];
      arr.push(e.source);
      dependsByTarget.set(e.target, arr);
    }

    const componentNodes = nodes.filter((n) => n.type === "component");
    const groupNodes = nodes.filter(isGroupNode);

    const components: ComponentInput[] = componentNodes.map((n) => {
      const c = (n.data as { component: EditableComponent }).component;
      const config: Record<string, string> = {};
      for (const [k, v] of Object.entries(c.config)) {
        if (v != null && v !== "") config[k] = v;
      }
      const group_id =
        n.parentId && groupIds.has(n.parentId) ? n.parentId : null;
      return {
        id: c.id,
        name: c.name,
        type: c.type,
        config,
        depends_on: dependsByTarget.get(n.id) ?? [],
        continue_on_failure: c.continue_on_failure,
        target_cluster_id: c.target_cluster_id,
        target_namespace: c.target_namespace,
        chart_credential_id: c.chart_credential_id,
        github_installation_id: c.github_installation_id,
        position: { x: Math.round(n.position.x), y: Math.round(n.position.y) },
        group_id,
      };
    });

    const groups: ComponentGroupInput[] = groupNodes.map((n) => ({
      id: n.id,
      name: (n.data as GroupNodeData).name,
      depends_on: dependsByTarget.get(n.id) ?? [],
      position: { x: Math.round(n.position.x), y: Math.round(n.position.y) },
      size: {
        w: Math.round(n.width ?? DEFAULT_GROUP_SIZE.w),
        h: Math.round(n.height ?? DEFAULT_GROUP_SIZE.h),
      },
    }));

    return { components, groups };
  }, [nodes, edges]);

  const save = useCallback(async () => {
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    const { components, groups } = buildPayload();
    const { data, error } = await api.PUT("/api/applications/{id}/workflow", {
      params: { path: { id: appId } },
      body: { components, groups },
    });
    setSaving(false);
    if (error || !data) {
      setSaveError(error?.message ?? "Could not save the workflow");
      return;
    }
    setSaved(true);
  }, [appId, buildPayload]);

  const startRun = useCallback(
    async (action: RunAction) => {
      setRunning(true);
      setRunError(null);
      const body =
        action === "deploy" ? { action, force: forceRoll } : { action };
      const { data, error, response } = await api.POST(
        "/api/applications/{id}/runs",
        { params: { path: { id: appId } }, body },
      );
      setRunning(false);
      if (error || !data) {
        if (response?.status === 409) {
          setRunError("A run is already in progress for this application.");
        } else {
          setRunError(error?.message ?? "Could not start the run");
        }
        return;
      }
      navigate(`/applications/${appId}/runs/${data.id}`);
    },
    [appId, forceRoll, navigate],
  );

  const value = useMemo<WorkflowDraftValue>(
    () => ({
      appId,
      canEdit,
      githubEnabled,
      nodes,
      edges,
      clusters,
      credentials,
      installations,
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
      updateComponent,
      deleteNode,
      getComponent,
      save,
      startRun,
    }),
    [
      appId,
      canEdit,
      githubEnabled,
      nodes,
      edges,
      clusters,
      credentials,
      installations,
      loading,
      error,
      saveError,
      saving,
      saved,
      runError,
      running,
      forceRoll,
      onNodesChange,
      onEdgesChange,
      onConnect,
      onNodeDragStop,
      addComponent,
      addGroup,
      updateComponent,
      deleteNode,
      getComponent,
      save,
      startRun,
    ],
  );

  return (
    <WorkflowDraftContext.Provider value={value}>
      {children}
    </WorkflowDraftContext.Provider>
  );
}
