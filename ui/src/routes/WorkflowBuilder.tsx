import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge,
  type EdgeChange,
  type Node,
  type NodeChange,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowLeft,
  History,
  Play,
  Plus,
  Save,
  Trash2,
} from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";
import { githubAppEnabled } from "../lib/appConfig";
import { BuilderNode, type BuilderNodeData } from "../components/workflow/nodes";
import {
  ConfigPanel,
  type EditableComponent,
} from "../components/workflow/ConfigPanel";

type Component = components["schemas"]["Component"];
type ComponentInput = components["schemas"]["ComponentInput"];
type ComponentType = components["schemas"]["ComponentType"];
type RunAction = components["schemas"]["RunAction"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];

const nodeTypes = { component: BuilderNode };

// Builder node type: React Flow node carrying our editable component as its
// data, keyed by the component id.
type FlowNode = Node<BuilderNodeData & { component: EditableComponent }>;

// toEditable strips position/depends_on (which live on the node + edges) off a
// loaded Component into the working shape the config panel edits.
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

// WorkflowBuilder is the interactive DAG canvas (route
// /applications/:appId/workflow). It loads the application's component graph,
// renders it with React Flow (nodes = components, edges = depends_on), lets the
// user add/edit/delete nodes and draw dependency edges, and persists it with
// PUT /workflow. Run controls start a deploy/preview/uninstall and land on the
// live run view. The server validates the DAG; its 400 surfaces inline.
export function WorkflowBuilder() {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";
  const githubEnabled = githubAppEnabled();

  const [nodes, setNodes] = useState<FlowNode[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);

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
  // Force a workload roll on the next deploy (helm upgrade --install --force).
  // Only meaningful for deploy; the planner ignores it for preview/uninstall.
  const [forceRoll, setForceRoll] = useState(false);

  // Load the workflow and lay nodes out from each component's persisted
  // position (falling back to a simple stagger when unset).
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
    const flowNodes: FlowNode[] = comps.map((c, i) => ({
      id: c.id,
      type: "component",
      position: {
        x: typeof c.position?.x === "number" ? c.position.x : 80 + (i % 4) * 240,
        y: typeof c.position?.y === "number" ? c.position.y : 80 + Math.floor(i / 4) * 160,
      },
      data: {
        name: c.name,
        type: c.type,
        continueOnFailure: c.continue_on_failure,
        component: toEditable(c),
      },
    }));
    // An edge per dependency: dep → node (the dependency points into the node).
    const flowEdges: Edge[] = [];
    for (const c of comps) {
      for (const dep of c.depends_on ?? []) {
        flowEdges.push({ id: `${dep}->${c.id}`, source: dep, target: c.id });
      }
    }
    setNodes(flowNodes);
    setEdges(flowEdges);
    setLoading(false);
  }, [appId]);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  // Reference data for the config panel's selects (same fetches as the app form).
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

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      setNodes((ns) => applyNodeChanges(changes, ns) as FlowNode[]);
      setSaved(false);
    },
    [],
  );
  const onEdgesChange = useCallback((changes: EdgeChange[]) => {
    setEdges((es) => applyEdgeChanges(changes, es));
    setSaved(false);
  }, []);
  const onConnect = useCallback((conn: Connection) => {
    if (!conn.source || !conn.target || conn.source === conn.target) return;
    setEdges((es) =>
      addEdge({ id: `${conn.source}->${conn.target}`, ...conn }, es),
    );
    setSaved(false);
  }, []);

  function addComponent(type: ComponentType) {
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
    setNodes((ns) => [
      ...ns,
      {
        id,
        type: "component",
        position: { x: 120 + ns.length * 40, y: 120 + ns.length * 40 },
        data: {
          name: editable.name,
          type: editable.type,
          continueOnFailure: false,
          component: editable,
        },
      },
    ]);
    setSelectedId(id);
    setSaved(false);
  }

  // Apply an edited component back onto its node (keeping the badge fields in
  // sync with the panel).
  function updateComponent(next: EditableComponent) {
    setNodes((ns) =>
      ns.map((n) =>
        n.id === next.id
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
  }

  function deleteSelected() {
    if (!selectedId) return;
    setNodes((ns) => ns.filter((n) => n.id !== selectedId));
    setEdges((es) =>
      es.filter((e) => e.source !== selectedId && e.target !== selectedId),
    );
    setSelectedId(null);
    setSaved(false);
  }

  const selected = useMemo(
    () => nodes.find((n) => n.id === selectedId)?.data.component ?? null,
    [nodes, selectedId],
  );

  // Assemble the PUT payload: config (with empty keys dropped), depends_on from
  // the inbound edges, and the live node position.
  function buildPayload(): ComponentInput[] {
    return nodes.map((n) => {
      const c = n.data.component;
      const config: Record<string, string> = {};
      for (const [k, v] of Object.entries(c.config)) {
        if (v != null && v !== "") config[k] = v;
      }
      const depends_on = edges
        .filter((e) => e.target === n.id)
        .map((e) => e.source);
      return {
        id: c.id,
        name: c.name,
        type: c.type,
        config,
        depends_on,
        continue_on_failure: c.continue_on_failure,
        target_cluster_id: c.target_cluster_id,
        target_namespace: c.target_namespace,
        chart_credential_id: c.chart_credential_id,
        github_installation_id: c.github_installation_id,
        position: { x: Math.round(n.position.x), y: Math.round(n.position.y) },
      };
    });
  }

  async function save() {
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    const { data, error } = await api.PUT("/api/applications/{id}/workflow", {
      params: { path: { id: appId } },
      body: { components: buildPayload() },
    });
    setSaving(false);
    if (error || !data) {
      setSaveError(error?.message ?? "Could not save the workflow");
      return;
    }
    setSaved(true);
  }

  async function startRun(action: RunAction) {
    setRunning(true);
    setRunError(null);
    // force only applies to deploy; the planner ignores it otherwise, but we
    // keep the body minimal and only send it where it's meaningful.
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
  }

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
              <button
                type="button"
                onClick={() => addComponent("helm")}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
              >
                <Plus className="h-3.5 w-3.5" />
                Helm
              </button>
              <button
                type="button"
                onClick={() => addComponent("manifest")}
                className="inline-flex items-center gap-1.5 border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
              >
                <Plus className="h-3.5 w-3.5" />
                Manifest
              </button>
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
        <div className="flex min-h-0 flex-1 border border-neutral-200">
          <div className="relative min-w-0 flex-1">
            {nodes.length === 0 && (
              <p className="absolute left-1/2 top-1/2 z-10 -translate-x-1/2 -translate-y-1/2 text-sm text-neutral-400">
                No components yet. Add a Helm or Manifest node to begin.
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
              onPaneClick={() => setSelectedId(null)}
              nodesConnectable={canEdit}
              fitView
            >
              <Background />
              <Controls />
            </ReactFlow>
          </div>
          {selected && canEdit && (
            <ConfigPanel
              component={selected}
              onChange={updateComponent}
              onDelete={deleteSelected}
              onClose={() => setSelectedId(null)}
              clusters={clusters}
              credentials={credentials}
              installations={installations}
              githubEnabled={githubEnabled}
            />
          )}
        </div>
      )}
    </div>
  );
}
