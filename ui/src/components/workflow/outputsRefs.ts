// Helpers for ${{ components.<name>.outputs.<key> }} references in the
// workflow editor: which OpenTofu components a component may reference (those
// in an earlier stage — the same rule the server enforces on save), and which
// components reference a given name (so a rename can warn before it breaks
// them). Pure functions over a minimal structural slice of the draft, unit
// tested without React.

// The slice of a draft stage these helpers read: its components, in order.
export interface RefStage {
  components: { id: string; name: string; type: string }[];
}

// upstreamTofuNames lists the names of the OpenTofu (terraform) components in
// stages before the one holding componentId — exactly the components whose
// outputs it may reference, since an earlier stage always finishes first. A
// component in the same stage runs in parallel, so it never qualifies. Sorted;
// empty when componentId isn't in any stage.
export function upstreamTofuNames(
  componentId: string,
  stages: RefStage[],
): string[] {
  const at = stages.findIndex((st) =>
    st.components.some((c) => c.id === componentId),
  );
  if (at < 0) return [];
  const names = new Set<string>();
  for (const st of stages.slice(0, at)) {
    for (const c of st.components) {
      if (c.type === "terraform" && c.name) names.add(c.name);
    }
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
