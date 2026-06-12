// Helpers for ${{ components.<name>.outputs.<key> }} references in the
// workflow editor: which OpenTofu components a node may reference (its
// transitive upstream dependencies — the same ancestry rule the server
// enforces on save), and which components reference a given name (so a rename
// can warn before it breaks them). Pure functions over a minimal structural
// slice of the canvas state, unit-tested without React Flow.

// The slice of a canvas node these helpers read. `kind` is the React Flow node
// type ("component" | "group"); `parentId` is group membership; `data` carries
// the component's display name and component type.
export interface RefGraphNode {
  id: string;
  type?: string;
  parentId?: string;
  data: { name?: string; type?: string };
}

// One dependency edge: target depends on source (source may be a group).
export interface RefGraphEdge {
  source: string;
  target: string;
}

// upstreamTofuNames lists the names of the OpenTofu (terraform) components
// that are transitive depends_on ancestors of nodeId, with group containers
// desugared the way the server expands them: depending on a group means
// depending on every member, and a group's dependencies apply to each member.
// These are exactly the components whose outputs the node may reference.
export function upstreamTofuNames(
  nodeId: string,
  nodes: RefGraphNode[],
  edges: RefGraphEdge[],
): string[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const groupIds = new Set(nodes.filter((n) => n.type === "group").map((n) => n.id));
  const members = new Map<string, string[]>();
  for (const n of nodes) {
    if (n.type !== "group" && n.parentId && groupIds.has(n.parentId)) {
      members.set(n.parentId, [...(members.get(n.parentId) ?? []), n.id]);
    }
  }
  // Inbound edge sources per target (component or group).
  const sourcesOf = new Map<string, string[]>();
  for (const e of edges) {
    sourcesOf.set(e.target, [...(sourcesOf.get(e.target) ?? []), e.source]);
  }
  // deps resolves one node's direct dependencies to component ids: its own
  // inbound edges plus its group's, with group sources expanded to members.
  function deps(id: string): string[] {
    const refs = [...(sourcesOf.get(id) ?? [])];
    const parent = byId.get(id)?.parentId;
    if (parent && groupIds.has(parent)) refs.push(...(sourcesOf.get(parent) ?? []));
    return refs.flatMap((ref) => (groupIds.has(ref) ? (members.get(ref) ?? []) : [ref]));
  }

  const seen = new Set<string>();
  const stack = deps(nodeId);
  while (stack.length > 0) {
    const id = stack.pop()!;
    if (id === nodeId || seen.has(id)) continue;
    seen.add(id);
    stack.push(...deps(id));
  }

  const names = new Set<string>();
  for (const id of seen) {
    const n = byId.get(id);
    if (n && n.data.type === "terraform" && n.data.name) names.add(n.data.name);
  }
  return [...names].sort();
}

// outputsRefSnippet is the reference the editor inserts for one upstream
// component — the key is left for the user to complete.
export function outputsRefSnippet(name: string): string {
  return "${{ components." + name + ".outputs. }}";
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// The slice of a component the reference scan reads: the three fields the
// server renders (inline values, release name, target namespace).
export interface RefScanComponent {
  name: string;
  config: Record<string, string>;
  target_namespace: string;
}

// componentsReferencing returns the names of the components whose interpolable
// fields reference `components.<name>.outputs.` — the rename check: renaming a
// referenced component breaks those references until they're updated too.
export function componentsReferencing(
  name: string,
  components: RefScanComponent[],
): string[] {
  if (!name) return [];
  const re = new RegExp(
    String.raw`\$\{\{\s*components\.` + escapeRegExp(name) + String.raw`\.outputs\.`,
  );
  return components
    .filter((c) =>
      [c.config.values ?? "", c.config.release_name ?? "", c.target_namespace ?? ""].some(
        (field) => re.test(field),
      ),
    )
    .map((c) => c.name);
}
