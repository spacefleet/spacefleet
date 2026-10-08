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
import { api } from "../api/client";
import { useOrg } from "./OrgContext";
import { useApplicationName } from "../lib/useApplicationName";
import type { components } from "../api/schema";
import { githubAppEnabled, managedStateEnabled } from "../lib/appConfig";
import { TOFU_SEED_VERSION } from "../lib/tofuVersions";
import type { EditableComponent } from "../components/workflow/ComponentFields";

type Component = components["schemas"]["Component"];
type ComponentInput = components["schemas"]["ComponentInput"];
type WorkflowStageInput = components["schemas"]["WorkflowStageInput"];
type ComponentType = components["schemas"]["ComponentType"];
type Cluster = components["schemas"]["Cluster"];
type ChartCredential = components["schemas"]["ChartCredential"];
type CloudCredential = components["schemas"]["CloudCredential"];
type GitHubInstallation = components["schemas"]["GitHubInstallation"];
type Variable = components["schemas"]["Variable"];
type ComponentOutputKeys = components["schemas"]["ComponentOutputKeys"];

// DraftStage is one stage of the in-memory workflow: its id, display name, and
// its components in display order. The stages array order is run order.
export interface DraftStage {
  id: string;
  name: string;
  components: EditableComponent[];
}

// seedComponent builds a fresh, empty editable component for a newly-added node
// of the given type. terraform is a single OpenTofu component that runs plan →
// apply: it carries no command (synthesized per run on the server) and defaults
// to gated (requires_approval = true → apply pauses for review); the editor
// frames that as an "Auto-approve apply" opt-out.
function seedComponent(id: string, type: ComponentType): EditableComponent {
  const base = {
    id,
    type,
    continue_on_failure: false,
    approval_policy: null,
    target_cluster_id: null,
    target_namespace: "",
    chart_credential_id: null,
    github_installation_id: null,
  };
  switch (type) {
    case "helm":
      return {
        ...base,
        name: "helm-release",
        config: { chart_source: "http_repo" },
        requires_approval: false,
      };
    case "terraform":
      // New components run the newest supported OpenTofu line — which locks
      // state automatically (native s3 locking); only pre-existing components
      // (no tofu_version) stay on the server's older default line. State
      // defaults to Spacefleet's managed backend (nothing to set up) when the
      // server can offer it, and to S3 otherwise.
      return {
        ...base,
        name: "opentofu",
        config: {
          backend: managedStateEnabled() ? "spacefleet" : "s3",
          tofu_version: TOFU_SEED_VERSION,
        },
        requires_approval: true,
      };
    default:
      return {
        ...base,
        name: "manifest-apply",
        config: {},
        requires_approval: false,
      };
  }
}

// toEditable copies a loaded Component into the working shape the editor edits.
function toEditable(c: Component): EditableComponent {
  return {
    id: c.id,
    name: c.name,
    type: c.type,
    config: { ...c.config },
    continue_on_failure: c.continue_on_failure,
    requires_approval: c.requires_approval ?? false,
    approval_policy: c.approval_policy ?? null,
    target_cluster_id: c.target_cluster_id ?? null,
    target_namespace: c.target_namespace ?? "",
    chart_credential_id: c.chart_credential_id ?? null,
    github_installation_id: c.github_installation_id ?? null,
  };
}

// toInput maps an editable component to the PUT payload shape, dropping blank
// config values (the server treats absent and empty alike, and a blank key
// would only add noise to the stored map).
function toInput(c: EditableComponent): ComponentInput {
  const config: Record<string, string> = {};
  for (const [k, v] of Object.entries(c.config)) {
    if (v != null && v !== "") config[k] = v;
  }
  return {
    id: c.id,
    name: c.name,
    type: c.type,
    config,
    continue_on_failure: c.continue_on_failure,
    requires_approval: c.requires_approval,
    approval_policy: c.approval_policy ?? undefined,
    target_cluster_id: c.target_cluster_id,
    target_namespace: c.target_namespace,
    chart_credential_id: c.chart_credential_id,
    github_installation_id: c.github_installation_id,
  };
}

// nextStageName picks "Stage N" for a new stage, N one past the highest number
// already used that way, so adding after a delete never repeats a name.
function nextStageName(stages: DraftStage[]): string {
  let n = stages.length;
  for (const st of stages) {
    const m = /^Stage (\d+)$/.exec(st.name);
    if (m) n = Math.max(n, Number(m[1]));
  }
  return `Stage ${n + 1}`;
}

function newStage(stages: DraftStage[]): DraftStage {
  return { id: crypto.randomUUID(), name: nextStageName(stages), components: [] };
}

interface WorkflowDraftValue {
  appId: string;
  // The application's display name (for the window title); undefined until
  // loaded.
  appName: string | undefined;
  canEdit: boolean;
  githubEnabled: boolean;

  stages: DraftStage[];
  clusters: Cluster[];
  credentials: ChartCredential[];
  cloudCredentials: CloudCredential[];
  installations: GitHubInstallation[];

  // Reference data for the ${{ }} autocomplete: the app-level variable names and
  // the known output keys per component id (latest successful run). Loaded once
  // per app/org; absent keys simply mean "no suggestions yet", never an error.
  appVariableNames: string[];
  componentOutputs: ComponentOutputKeys;

  loading: boolean;
  error: string | null;
  saveError: string | null;
  // Set (to the server's explanation) when the last save was refused because
  // it moves an OpenTofu component that still manages resources to another
  // state backend. Auto-save holds off until the author confirms the switch
  // (confirmBackendChange) or edits the draft again.
  backendChange: string | null;
  saving: boolean;
  saved: boolean;
  // Set when a just-saved component's staged variables couldn't be flushed to
  // their endpoints (e.g. encryption disabled) — the workflow itself still saved.
  varFlushError: string | null;

  addStage: () => void;
  renameStage: (id: string, name: string) => void;
  // Removes the stage and every component in it.
  deleteStage: (id: string) => void;
  // Moves a stage one place earlier (-1) or later (+1) in run order.
  moveStage: (id: string, delta: -1 | 1) => void;

  // addComponent navigates to the editor's create route for a new component of
  // the given type in the given stage; creation itself is driven by the route
  // (see ensureProvisional).
  addComponent: (stageId: string, type: ComponentType) => void;
  // moveComponent places a component in a stage at toIndex — an index into
  // that stage's components with the moved one already taken out.
  moveComponent: (componentId: string, toStageId: string, toIndex: number) => void;
  updateComponent: (next: EditableComponent) => void;
  deleteComponent: (id: string) => void;
  getComponent: (id: string) => EditableComponent | null;
  // The stage holding a component (and its position in run order), or null.
  stageOf: (componentId: string) => { stage: DraftStage; index: number } | null;

  // A freshly added component is "provisional": it shows in its stage but is
  // excluded from the saved payload until the editor commits it, so backing out
  // of the editor leaves nothing behind. The editor drives these.
  isProvisional: (id: string) => boolean;
  // ensureProvisional creates the provisional component for a given id+type in
  // the given stage if it doesn't already exist. The editor calls it on mount
  // so a deep link / page reload to a not-yet-saved create page re-seeds a
  // fresh component instead of showing "not found"; a stage id that no longer
  // exists falls back to the last stage (or a new first stage).
  ensureProvisional: (id: string, type: ComponentType, stageId: string | null) => void;
  commitComponent: (next: EditableComponent) => void;
  discardNewNode: (id: string) => void;

  // Staged component variables: a not-yet-saved component can't write to its
  // variable endpoints (the component row doesn't exist yet), so the editor
  // stages them here, keyed by component id. The next successful workflow save
  // flushes them to the real create endpoint (see save()).
  getStagedVars: (componentId: string) => Variable[];
  setStagedVars: (componentId: string, vars: Variable[]) => void;

  save: () => Promise<void>;
  // Re-saves with the backend switch confirmed (see backendChange).
  confirmBackendChange: () => Promise<void>;
}

const WorkflowDraftContext = createContext<WorkflowDraftValue | null>(null);

// useWorkflowDraft reads the in-memory workflow draft shared across the stage
// builder and the full-page component editor. Throws if used outside the
// provider.
// eslint-disable-next-line react-refresh/only-export-components
export function useWorkflowDraft(): WorkflowDraftValue {
  const ctx = useContext(WorkflowDraftContext);
  if (!ctx)
    throw new Error(
      "useWorkflowDraft must be used within a WorkflowDraftProvider",
    );
  return ctx;
}

// WorkflowDraftProvider owns the whole workflow draft (stages + components +
// reference data + save state) so unsaved edits survive navigating between the
// builder and the full-page component editor (which live on nested routes
// under one layout). It loads the workflow on mount so a deep link to the
// editor route works, and reloads when the org changes.
export function WorkflowDraftProvider({ children }: { children: ReactNode }) {
  const { appId = "" } = useParams();
  const { currentOrg, currentRole } = useOrg();
  const navigate = useNavigate();
  const canEdit = currentRole !== "viewer";
  const githubEnabled = githubAppEnabled();
  const appName = useApplicationName(appId);

  const [stages, setStages] = useState<DraftStage[]>([]);

  // Ids of newly added components the editor hasn't committed yet (see
  // isProvisional / commitComponent / discardNewNode). They show in their stage
  // but are kept out of the save payload until committed, so an abandoned add
  // never persists.
  const [provisional, setProvisional] = useState<Set<string>>(() => new Set());

  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [credentials, setCredentials] = useState<ChartCredential[]>([]);
  const [cloudCredentials, setCloudCredentials] = useState<CloudCredential[]>(
    [],
  );
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
  const [appVariableNames, setAppVariableNames] = useState<string[]>([]);
  const [componentOutputs, setComponentOutputs] = useState<ComponentOutputKeys>(
    {},
  );

  // Staged component variables for not-yet-saved components, keyed by component
  // id. A ref (not state): the editor owns the on-screen list, this is just the
  // durable buffer that survives navigating between editor and builder and is
  // drained by the next successful save. discardNewNode drops a node's entry.
  const stagedVarsRef = useRef<Map<string, Variable[]>>(new Map());

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [backendChange, setBackendChange] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [varFlushError, setVarFlushError] = useState<string | null>(null);

  // markDirty flags the draft as having unsaved edits and bumps an edit revision.
  // The revision lets a save that resolves after newer edits avoid stamping the
  // draft "saved" (it would mask those edits) — instead the auto-save effect runs
  // again. Every genuine edit funnels through here.
  const revision = useRef(0);
  const markDirty = useCallback(() => {
    revision.current += 1;
    setSaved(false);
    // A fresh edit clears a prior save error so auto-save gets another chance
    // (the effect holds off while an error is showing to avoid a retry storm).
    setSaveError(null);
    setBackendChange(null);
  }, []);

  // Load the workflow. An application with no stages yet gets one empty local
  // "Stage 1" so there is somewhere to add the first component — it isn't
  // saved until something is actually added (the draft loads clean).
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
    const loaded: DraftStage[] = data.stages.map((st) => ({
      id: st.id,
      name: st.name,
      components: st.components.map(toEditable),
    }));
    setStages(loaded.length > 0 ? loaded : [newStage([])]);
    // Everything just loaded matches the server, so nothing is pending.
    setProvisional(new Set());
    // The freshly loaded draft matches the server, so it's clean — important so
    // auto-save doesn't fire on load (the initial saved=false is just the
    // pre-load placeholder).
    setSaved(true);
    setLoading(false);
  }, [appId]);

  // Load on mount and whenever the org changes. Re-running on org change also
  // resets any unsaved draft, which is correct because the draft belongs to the
  // previously-selected org.
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

  // App-scoped reference data for the ${{ }} autocomplete. App-level variable
  // names feed vars.* suggestions; component-outputs feeds the known output keys
  // for components.<name>.outputs.* (editor-gated — a viewer just gets an empty
  // map, which is fine since the autocomplete only matters while editing).
  useEffect(() => {
    if (!appId) return;
    void (async () => {
      const { data } = await api.GET("/api/applications/{id}/variables", {
        params: { path: { id: appId } },
      });
      setAppVariableNames((data ?? []).map((v) => v.name));
    })();
    void (async () => {
      const { data } = await api.GET(
        "/api/applications/{id}/component-outputs",
        {
          params: { path: { id: appId } },
        },
      );
      setComponentOutputs(data ?? {});
    })();
  }, [appId, currentOrg?.id]);

  const addStage = useCallback(() => {
    setStages((ss) => [...ss, newStage(ss)]);
    markDirty();
  }, [markDirty]);

  const renameStage = useCallback(
    (id: string, name: string) => {
      setStages((ss) => ss.map((st) => (st.id === id ? { ...st, name } : st)));
      markDirty();
    },
    [markDirty],
  );

  const deleteStage = useCallback(
    (id: string) => {
      // Drop any staged variables of the removed components so they can't
      // flush onto a later component that reuses the id.
      const gone = stages.find((st) => st.id === id);
      for (const c of gone?.components ?? []) stagedVarsRef.current.delete(c.id);
      setStages((ss) => ss.filter((st) => st.id !== id));
      markDirty();
    },
    [stages, markDirty],
  );

  const moveStage = useCallback(
    (id: string, delta: -1 | 1) => {
      setStages((ss) => {
        const i = ss.findIndex((st) => st.id === id);
        const j = i + delta;
        if (i < 0 || j < 0 || j >= ss.length) return ss;
        const next = [...ss];
        [next[i], next[j]] = [next[j], next[i]];
        return next;
      });
      markDirty();
    },
    [markDirty],
  );

  // addComponent navigates to the editor's create route (carrying the type and
  // stage in query params) WITHOUT mutating state. The editor calls
  // ensureProvisional on mount to create the component, so creation is driven
  // entirely by the route — a page reload on the create page re-seeds a fresh
  // component instead of landing on "not found".
  const addComponent = useCallback(
    (stageId: string, type: ComponentType) => {
      const id = crypto.randomUUID();
      navigate(
        `/applications/${appId}/workflow/nodes/${id}?new=${type}&stage=${stageId}`,
      );
    },
    [appId, navigate],
  );

  // ensureProvisional creates the provisional component for id+type if it
  // isn't already in the draft (idempotent — safe to call repeatedly, e.g.
  // across a StrictMode double-effect or a reload). It's provisional until the
  // editor commits it — no markDirty, so an abandoned add never auto-saves (the
  // component is excluded from the payload while provisional).
  const ensureProvisional = useCallback(
    (id: string, type: ComponentType, stageId: string | null) => {
      // A committed component is left alone.
      if (stages.some((st) => st.components.some((c) => c.id === id))) return;
      const editable = seedComponent(id, type);
      setStages((ss) => {
        if (ss.some((st) => st.components.some((c) => c.id === id))) return ss;
        let target = ss.findIndex((st) => st.id === stageId);
        let next = ss;
        if (target < 0) {
          // The stage is gone (or the URL never named one): use the last
          // stage, or start one if there are none.
          if (ss.length === 0) next = [newStage([])];
          target = next.length - 1;
        }
        return next.map((st, i) =>
          i === target ? { ...st, components: [...st.components, editable] } : st,
        );
      });
      setProvisional((p) => (p.has(id) ? p : new Set(p).add(id)));
    },
    [stages],
  );

  const moveComponent = useCallback(
    (componentId: string, toStageId: string, toIndex: number) => {
      setStages((ss) => {
        const moving = ss
          .flatMap((st) => st.components)
          .find((c) => c.id === componentId);
        if (!moving || !ss.some((st) => st.id === toStageId)) return ss;
        const without = ss.map((st) => ({
          ...st,
          components: st.components.filter((c) => c.id !== componentId),
        }));
        return without.map((st) => {
          if (st.id !== toStageId) return st;
          const at = Math.max(0, Math.min(toIndex, st.components.length));
          const comps = [...st.components];
          comps.splice(at, 0, moving);
          return { ...st, components: comps };
        });
      });
      markDirty();
    },
    [markDirty],
  );

  const updateComponent = useCallback(
    (next: EditableComponent) => {
      setStages((ss) =>
        ss.map((st) => ({
          ...st,
          components: st.components.map((c) => (c.id === next.id ? next : c)),
        })),
      );
      markDirty();
    },
    [markDirty],
  );

  const deleteComponent = useCallback(
    (id: string) => {
      setStages((ss) =>
        ss.map((st) => ({
          ...st,
          components: st.components.filter((c) => c.id !== id),
        })),
      );
      stagedVarsRef.current.delete(id);
      markDirty();
    },
    [markDirty],
  );

  const getComponent = useCallback(
    (id: string): EditableComponent | null => {
      for (const st of stages) {
        const c = st.components.find((x) => x.id === id);
        if (c) return c;
      }
      return null;
    },
    [stages],
  );

  const stageOf = useCallback(
    (componentId: string) => {
      const index = stages.findIndex((st) =>
        st.components.some((c) => c.id === componentId),
      );
      return index < 0 ? null : { stage: stages[index], index };
    },
    [stages],
  );

  const isProvisional = useCallback(
    (id: string) => provisional.has(id),
    [provisional],
  );

  // commitComponent is the editor's Save: it writes the working copy back into
  // the draft and clears the component's provisional flag, so it now travels in
  // the save payload and the debounced auto-save persists it.
  const commitComponent = useCallback(
    (next: EditableComponent) => {
      updateComponent(next);
      if (!provisional.has(next.id)) return;
      setProvisional((prev) => {
        const n = new Set(prev);
        n.delete(next.id);
        return n;
      });
    },
    [updateComponent, provisional],
  );

  // discardNewNode is the editor's Cancel for a component that was never
  // committed: it removes the component from its stage. A no-op for an
  // already-committed component — backing out of those just drops the editor's
  // local edits without touching the draft.
  const discardNewNode = useCallback(
    (id: string) => {
      if (!provisional.has(id)) return;
      setStages((ss) =>
        ss.map((st) => ({
          ...st,
          components: st.components.filter((c) => c.id !== id),
        })),
      );
      setProvisional((prev) => {
        const n = new Set(prev);
        n.delete(id);
        return n;
      });
      // Drop any staged variables for the abandoned component so they don't
      // flush onto some later component that reuses the (random) id.
      stagedVarsRef.current.delete(id);
    },
    [provisional],
  );

  // Staged component variables (see the interface): a stable ref-backed buffer,
  // so reads/writes don't re-render the provider and survive editor⇄builder
  // navigation. The editor's in-memory VariablesEditor reads/writes these.
  const getStagedVars = useCallback(
    (componentId: string): Variable[] =>
      stagedVarsRef.current.get(componentId) ?? [],
    [],
  );
  const setStagedVars = useCallback((componentId: string, vars: Variable[]) => {
    if (vars.length === 0) stagedVarsRef.current.delete(componentId);
    else stagedVarsRef.current.set(componentId, vars);
  }, []);

  // flushStagedVars POSTs each staged variable for the components a save just
  // persisted (their ids are in sentIds), then clears the ones that landed.
  // Failures are kept buffered (so the next save retries) and summarized in
  // varFlushError — never failing the workflow save, which already succeeded.
  const flushStagedVars = useCallback(
    async (sentIds: Set<string>) => {
      const failures: string[] = [];
      for (const [componentId, vars] of stagedVarsRef.current) {
        if (!sentIds.has(componentId)) continue;
        const remaining: Variable[] = [];
        for (const v of vars) {
          const { error } = await api.POST(
            "/api/applications/{id}/components/{componentId}/variables",
            {
              params: { path: { id: appId, componentId } },
              body: {
                name: v.name,
                value: v.value ?? "",
                sensitive: v.sensitive,
              },
            },
          );
          if (error) {
            failures.push(`${v.name}: ${error.message ?? "could not save"}`);
            remaining.push(v);
          }
        }
        if (remaining.length > 0)
          stagedVarsRef.current.set(componentId, remaining);
        else stagedVarsRef.current.delete(componentId);
      }
      setVarFlushError(
        failures.length > 0
          ? `Some component variables could not be saved — ${failures.join("; ")}`
          : null,
      );
    },
    [appId],
  );

  // Assemble the PUT payload: every stage in order with its committed
  // components in order. Provisional components are left out until the editor
  // commits them.
  const buildPayload = useCallback(
    (): WorkflowStageInput[] =>
      stages.map((st) => ({
        id: st.id,
        name: st.name,
        components: st.components
          .filter((c) => !provisional.has(c.id))
          .map(toInput),
      })),
    [stages, provisional],
  );

  const persist = useCallback(async (allowBackendChange: boolean) => {
    const rev = revision.current;
    setSaving(true);
    setSaveError(null);
    setBackendChange(null);
    const payload = buildPayload();
    const { data, error } = await api.PUT("/api/applications/{id}/workflow", {
      params: { path: { id: appId } },
      body: {
        stages: payload,
        ...(allowBackendChange ? { allow_backend_change: true } : {}),
      },
    });
    setSaving(false);
    if (error?.code === "backend_change") {
      setBackendChange(error.message);
      return;
    }
    if (error || !data) {
      setSaveError(error?.message ?? "Could not save the workflow");
      return;
    }
    // Only mark clean if no edits landed while this save was in flight; otherwise
    // leave it dirty so the auto-save effect runs again for the newer state.
    if (revision.current === rev) setSaved(true);
    // The components in this payload now exist server-side, so any variables
    // staged for them can be flushed to their endpoints.
    await flushStagedVars(
      new Set(payload.flatMap((st) => st.components.map((c) => c.id))),
    );
  }, [appId, buildPayload, flushStagedVars]);
  const save = useCallback(() => persist(false), [persist]);
  const confirmBackendChange = useCallback(() => persist(true), [persist]);

  // Auto-save: whenever the draft is dirty (and we can edit), schedule a debounced
  // save. `save`'s identity changes with every edit (it closes over the payload),
  // so this effect re-runs and resets the timer on each change — that's the
  // debounce. A save in flight (saving) or a clean draft (saved) short-circuits.
  useEffect(() => {
    if (!canEdit || loading || saving || saved || error || saveError || backendChange) return;
    const t = setTimeout(() => void save(), 800);
    return () => clearTimeout(t);
  }, [canEdit, loading, saving, saved, error, saveError, backendChange, save]);

  const value = useMemo<WorkflowDraftValue>(
    () => ({
      appId,
      appName,
      canEdit,
      githubEnabled,
      stages,
      clusters,
      credentials,
      cloudCredentials,
      installations,
      appVariableNames,
      componentOutputs,
      loading,
      error,
      saveError,
      backendChange,
      saving,
      saved,
      varFlushError,
      addStage,
      renameStage,
      deleteStage,
      moveStage,
      addComponent,
      moveComponent,
      updateComponent,
      deleteComponent,
      getComponent,
      stageOf,
      isProvisional,
      ensureProvisional,
      commitComponent,
      discardNewNode,
      getStagedVars,
      setStagedVars,
      save,
      confirmBackendChange,
    }),
    [
      appId,
      appName,
      canEdit,
      githubEnabled,
      stages,
      clusters,
      credentials,
      cloudCredentials,
      installations,
      appVariableNames,
      componentOutputs,
      loading,
      error,
      saveError,
      backendChange,
      saving,
      saved,
      varFlushError,
      addStage,
      renameStage,
      deleteStage,
      moveStage,
      addComponent,
      moveComponent,
      updateComponent,
      deleteComponent,
      getComponent,
      stageOf,
      isProvisional,
      ensureProvisional,
      commitComponent,
      discardNewNode,
      getStagedVars,
      setStagedVars,
      save,
      confirmBackendChange,
    ],
  );

  return (
    <WorkflowDraftContext.Provider value={value}>
      {children}
    </WorkflowDraftContext.Provider>
  );
}
