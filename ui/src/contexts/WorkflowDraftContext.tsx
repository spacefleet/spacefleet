import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
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
type CloudCredential = components["schemas"]["CloudCredential"];
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

// Which React Flow node changes actually alter persisted state. Selection and
// the measurement "dimensions" pass React Flow emits on mount must NOT mark the
// draft dirty — otherwise auto-save would fire on every load and every click. A
// "dimensions" change with a defined `resizing` flag comes from the user dragging
// the NodeResizer, which we do persist.
function isPersistentNodeChange(c: NodeChange): boolean {
  if (c.type === "position") return true;
  if (c.type === "remove" || c.type === "add" || c.type === "replace")
    return true;
  if (c.type === "dimensions") return c.resizing !== undefined;
  return false; // "select"
}

// Edge selection isn't persisted; add/remove/replace are.
function isPersistentEdgeChange(c: EdgeChange): boolean {
  return c.type !== "select";
}

// groupAt finds the group whose bounds contain the absolute point (x, y), so a
// dragged node can be matched to the group it's hovering over / dropped into.
function groupAt(ns: FlowNode[], x: number, y: number): FlowNode | undefined {
  return ns.find((g) => {
    if (!isGroupNode(g)) return false;
    const w = g.width ?? DEFAULT_GROUP_SIZE.w;
    const h = g.height ?? DEFAULT_GROUP_SIZE.h;
    return (
      x >= g.position.x &&
      x <= g.position.x + w &&
      y >= g.position.y &&
      y <= g.position.y + h
    );
  });
}

// absPos resolves a node's absolute canvas position, accounting for its parent
// (child positions are stored relative to the parent group).
function absPos(ns: FlowNode[], n: FlowNode): { x: number; y: number } {
  const parent = n.parentId ? ns.find((p) => p.id === n.parentId) : undefined;
  return {
    x: (parent?.position.x ?? 0) + n.position.x,
    y: (parent?.position.y ?? 0) + n.position.y,
  };
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
    requires_approval: c.requires_approval ?? false,
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
  cloudCredentials: CloudCredential[];
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

  // A request to frame specific node ids in the viewport, bumped (via nonce) each
  // time so the canvas re-fits even when the same ids are focused twice. The
  // canvas consumes this with fitView; the provider only records the intent
  // because it can't call into React Flow's instance itself.
  focus: { ids: string[]; nonce: number } | null;

  onNodesChange: (changes: NodeChange[]) => void;
  onEdgesChange: (changes: EdgeChange[]) => void;
  onConnect: (conn: Connection) => void;
  onNodeDrag: (node: Node) => void;
  onNodeDragStop: (node: Node) => void;

  addComponent: (type: ComponentType) => void;
  addTerraform: () => void;
  addGroup: () => void;
  groupSelection: (ids: string[]) => void;
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
  const [cloudCredentials, setCloudCredentials] = useState<CloudCredential[]>([]);
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [forceRoll, setForceRoll] = useState(false);

  // Viewport-focus request: when something adds/groups nodes we ask the canvas to
  // frame them. The nonce (a ref-backed counter, since Date.now/Math.random are
  // unavailable here and would be overkill anyway) guarantees a fresh object so
  // the canvas effect refires even when the same ids are focused twice.
  const [focus, setFocus] = useState<{ ids: string[]; nonce: number } | null>(
    null,
  );
  const focusNonce = useRef(0);
  const requestFocus = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    focusNonce.current += 1;
    setFocus({ ids, nonce: focusNonce.current });
  }, []);

  // markDirty flags the draft as having unsaved edits and bumps an edit revision.
  // The revision lets a save that resolves after newer edits avoid stamping the
  // draft "saved" (it would mask those edits) — instead the auto-save effect runs
  // again. Every genuine edit funnels through here; selection/measurement do not.
  const revision = useRef(0);
  const markDirty = useCallback(() => {
    revision.current += 1;
    setSaved(false);
    // A fresh edit clears a prior save error so auto-save gets another chance
    // (the effect holds off while an error is showing to avoid a retry storm).
    setSaveError(null);
  }, []);

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
        ...(parentId
          ? { parentId, extent: "parent" as const, expandParent: true }
          : {}),
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
    // The freshly loaded draft matches the server, so it's clean — important so
    // auto-save doesn't fire on load (the initial saved=false is just the
    // pre-load placeholder).
    setSaved(true);
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
    void (async () => {
      const { data } = await api.GET("/api/cloud-credentials");
      setCloudCredentials(data ?? []);
    })();
    if (githubEnabled) {
      void (async () => {
        const { data } = await api.GET("/api/github/installations");
        setInstallations(data ?? []);
      })();
    }
  }, [githubEnabled, currentOrg?.id]);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      setNodes((ns) => applyNodeChanges(changes, ns) as FlowNode[]);
      if (changes.some(isPersistentNodeChange)) markDirty();
    },
    [markDirty],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      setEdges((es) => applyEdgeChanges(changes, es));
      if (changes.some(isPersistentEdgeChange)) markDirty();
    },
    [markDirty],
  );
  const onConnect = useCallback(
    (conn: Connection) => {
      if (!conn.source || !conn.target || conn.source === conn.target) return;
      setEdges((es) =>
        addEdge({ id: `${conn.source}->${conn.target}`, ...conn }, es),
      );
      markDirty();
    },
    [markDirty],
  );

  // clearDropHighlights removes the transient isDropTarget flag from every group,
  // returning the same array reference when nothing changed (so callers can skip
  // a needless re-render).
  const clearDropHighlights = useCallback((ns: FlowNode[]): FlowNode[] => {
    let changed = false;
    const next = ns.map((n) => {
      if (isGroupNode(n) && (n.data as GroupNodeData).isDropTarget) {
        changed = true;
        return { ...n, data: { ...n.data, isDropTarget: false } };
      }
      return n;
    });
    return changed ? next : ns;
  }, []);

  // While a component node is being dragged, light up the group it's hovering
  // over so it's obvious the group is a drop target. We only rewrite group data
  // when the target actually changes, leaving the dragged node's position to the
  // normal onNodesChange flow.
  const onNodeDrag = useCallback((dragged: Node) => {
    setNodes((ns) => {
      const node = ns.find((n) => n.id === dragged.id) as FlowNode | undefined;
      if (!node || isGroupNode(node)) return clearDropHighlights(ns);

      // dragged.position is live (and parent-relative when the node has a parent).
      const parent = node.parentId
        ? ns.find((n) => n.id === node.parentId)
        : undefined;
      const x = (parent?.position.x ?? 0) + dragged.position.x;
      const y = (parent?.position.y ?? 0) + dragged.position.y;
      const targetId = groupAt(ns, x, y)?.id;

      let changed = false;
      const next = ns.map((n) => {
        if (!isGroupNode(n)) return n;
        const should = n.id === targetId;
        if (((n.data as GroupNodeData).isDropTarget ?? false) !== should) {
          changed = true;
          return { ...n, data: { ...n.data, isDropTarget: should } };
        }
        return n;
      });
      return changed ? next : ns;
    });
  }, [clearDropHighlights]);

  // On drag stop, decide group membership: if a component node's bounds land
  // inside a group node, adopt it (parentId + position relative to the parent,
  // expandParent so the group grows to keep it enclosed); if dragged clear of
  // every group, detach it. Group nodes themselves never become children. Any
  // drop-target highlight from the drag is cleared.
  const onNodeDragStop = useCallback((dragged: Node) => {
    setNodes((prev) => {
      const ns = clearDropHighlights(prev);
      const node = ns.find((n) => n.id === dragged.id) as FlowNode | undefined;
      if (!node || isGroupNode(node)) return ns;

      const abs = absPos(ns, node);
      const target = groupAt(ns, abs.x, abs.y);
      const nextParentId = target?.id;
      if (nextParentId === node.parentId) return ns; // no membership change

      markDirty();
      return ns.map((n) => {
        if (n.id !== node.id) return n;
        if (nextParentId) {
          const g = ns.find((x) => x.id === nextParentId)!;
          return {
            ...n,
            parentId: nextParentId,
            extent: "parent" as const,
            expandParent: true,
            position: { x: abs.x - g.position.x, y: abs.y - g.position.y },
          };
        }
        // Detached: restore absolute position, drop parent linkage.
        const rest = { ...n, position: abs };
        delete rest.parentId;
        delete rest.extent;
        delete rest.expandParent;
        return rest;
      });
    });
  }, [clearDropHighlights, markDirty]);

  const addComponent = useCallback((type: ComponentType) => {
    const id = crypto.randomUUID();
    const editable: EditableComponent = {
      id,
      name: type === "helm" ? "helm release" : "manifest apply",
      type,
      config: type === "helm" ? { chart_source: "http_repo" } : {},
      continue_on_failure: false,
      requires_approval: false,
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
    markDirty();
    // Go straight to the node's editor so the user fills it in immediately,
    // rather than landing on a half-configured node on the canvas. (No
    // requestFocus: we're navigating off the canvas, so there's nothing to fit.)
    navigate(`/applications/${appId}/workflow/nodes/${id}`);
  }, [appId, navigate, markDirty]);

  // addTerraform adds a terraform deployment as TWO linked nodes — a plan node
  // and an apply node that depends on it — plus the connecting edge (depends_on
  // is derived from edges at save time, so the edge is what records the link).
  // The apply node defaults to requires_approval=true so the run parks for a
  // human to review the plan before applying. Both nodes are seeded with the
  // same git source + backend so the user fills the repo details once on the
  // plan node and copies them to apply (per-node config is the v1 tradeoff).
  const addTerraform = useCallback(() => {
    const planId = crypto.randomUUID();
    const applyId = crypto.randomUUID();
    const seed: Record<string, string> = {
      repo_url: "",
      git_ref: "",
      path: "",
      backend: "kubernetes",
    };
    const plan: EditableComponent = {
      id: planId,
      name: "terraform plan",
      type: "terraform",
      config: { ...seed, command: "plan" },
      continue_on_failure: false,
      requires_approval: false,
      target_cluster_id: null,
      target_namespace: "",
      chart_credential_id: null,
      github_installation_id: null,
    };
    const apply: EditableComponent = {
      id: applyId,
      name: "terraform apply",
      type: "terraform",
      config: { ...seed, command: "apply" },
      continue_on_failure: false,
      requires_approval: true,
      target_cluster_id: null,
      target_namespace: "",
      chart_credential_id: null,
      github_installation_id: null,
    };
    setNodes((ns) => {
      const slot = ns.filter((n) => n.type === "component" && !n.parentId).length;
      const x = 80 + (slot % 4) * 240;
      const y = 80 + Math.floor(slot / 4) * 160;
      const mk = (c: EditableComponent, dy: number): FlowNode => ({
        id: c.id,
        type: "component",
        position: { x, y: y + dy },
        data: {
          name: c.name,
          type: c.type,
          continueOnFailure: c.continue_on_failure,
          component: c,
        },
      });
      return [...ns, mk(plan, 0), mk(apply, 160)];
    });
    // The edge plan -> apply is what buildPayload turns into apply.depends_on.
    setEdges((es) => [...es, { id: `${planId}->${applyId}`, source: planId, target: applyId }]);
    markDirty();
    // Open the plan node's editor first — the user fills the repo/source there,
    // then copies it to apply (per-node config is the v1 tradeoff).
    navigate(`/applications/${appId}/workflow/nodes/${planId}`);
  }, [appId, navigate, markDirty]);

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
    markDirty();
    requestFocus([id]);
  }, [markDirty, requestFocus]);

  // groupSelection wraps the given component nodes in a new group sized to
  // enclose them (with padding for the header), reparenting each one. This is the
  // shift-select path: pick nodes on the canvas, then hit "Group". Selected group
  // nodes are ignored; a node already inside another group is moved into the new
  // one. The group is prepended so it renders behind — and lists before — its
  // children, satisfying React Flow's parent-before-child ordering.
  const groupSelection = useCallback(
    (ids: string[]) => {
      const idSet = new Set(ids);
      const gid = crypto.randomUUID();
      setNodes((ns) => {
        const members = ns.filter(
          (n) => n.type === "component" && idSet.has(n.id),
        );
        if (members.length === 0) return ns;

        // Approximate node footprint (min-w-[11rem] ≈ 176px wide, ~64px tall).
        const NODE_W = 176;
        const NODE_H = 64;
        const PAD = 28;
        const HEADER = 24;

        let minX = Infinity;
        let minY = Infinity;
        let maxX = -Infinity;
        let maxY = -Infinity;
        for (const m of members) {
          const a = absPos(ns, m);
          minX = Math.min(minX, a.x);
          minY = Math.min(minY, a.y);
          maxX = Math.max(maxX, a.x + NODE_W);
          maxY = Math.max(maxY, a.y + NODE_H);
        }

        const gx = minX - PAD;
        const gy = minY - PAD - HEADER;
        const gw = maxX - minX + PAD * 2;
        const gh = maxY - minY + PAD * 2 + HEADER;
        const group: FlowNode = {
          id: gid,
          type: "group",
          position: { x: gx, y: gy },
          width: gw,
          height: gh,
          data: { name: "group" },
        };

        const reparented = ns.map((n) => {
          if (!idSet.has(n.id) || n.type !== "component") return n;
          const a = absPos(ns, n);
          return {
            ...n,
            parentId: gid,
            extent: "parent" as const,
            expandParent: true,
            position: { x: a.x - gx, y: a.y - gy },
          };
        });
        return [group, ...reparented];
      });
      markDirty();
      requestFocus([gid]);
    },
    [markDirty, requestFocus],
  );

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
    markDirty();
  }, [markDirty]);

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
    markDirty();
  }, [markDirty]);

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
        requires_approval: c.requires_approval,
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
    const rev = revision.current;
    setSaving(true);
    setSaveError(null);
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
    // Only mark clean if no edits landed while this save was in flight; otherwise
    // leave it dirty so the auto-save effect runs again for the newer state.
    if (revision.current === rev) setSaved(true);
  }, [appId, buildPayload]);

  // Auto-save: whenever the draft is dirty (and we can edit), schedule a debounced
  // save. `save`'s identity changes with every node/edge edit (it closes over the
  // payload), so this effect re-runs and resets the timer on each change — that's
  // the debounce. A save in flight (saving) or a clean draft (saved) short-circuits.
  useEffect(() => {
    if (!canEdit || loading || saving || saved || error || saveError) return;
    const t = setTimeout(() => void save(), 800);
    return () => clearTimeout(t);
  }, [canEdit, loading, saving, saved, error, saveError, save]);

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
      cloudCredentials,
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
      focus,
      onNodesChange,
      onEdgesChange,
      onConnect,
      onNodeDrag,
      onNodeDragStop,
      addComponent,
      addTerraform,
      addGroup,
      groupSelection,
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
      cloudCredentials,
      installations,
      loading,
      error,
      saveError,
      saving,
      saved,
      runError,
      running,
      forceRoll,
      focus,
      onNodesChange,
      onEdgesChange,
      onConnect,
      onNodeDrag,
      onNodeDragStop,
      addComponent,
      addTerraform,
      addGroup,
      groupSelection,
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
