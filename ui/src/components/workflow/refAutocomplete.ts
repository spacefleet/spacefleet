// Pure helpers for the inline `${{ … }}` reference autocomplete used by the
// helm interpolable fields (values, release name, target namespace). They mirror
// the server-side grammar in lib/interpolate/interpolate.go: three namespaces —
// `vars.<NAME>`, `run.<key>`, `components.<name>.outputs.<key>` — with `$${{`
// as the escaped literal. Kept React-free so they can be unit-tested directly;
// the RefAutocompleteField component is the thin view on top.

// OutputKeyInfo is one known output key for a component (from its latest
// successful run). The value is never carried — only the name, sensitivity, and
// an optional type hint — matching the keys-only component-outputs endpoint.
export interface OutputKeyInfo {
  key: string;
  sensitive: boolean;
  type?: string;
}

// RefContext is everything the suggester knows at authoring time: variable names
// (app + this component), the upstream OpenTofu component names this node may
// reference, and any known output keys per component name. Run keys are static
// (RUN_KEYS) so they aren't passed in.
export interface RefContext {
  varsNames: string[];
  componentNames: string[];
  outputKeysByName: Record<string, OutputKeyInfo[]>;
}

// The static run-context keys, with a short description each. Mirrors the
// `run.*` keys the server resolves (see interpolate.go / the values help text).
export const RUN_KEYS: { key: string; detail: string }[] = [
  { key: "id", detail: "the run's id" },
  { key: "action", detail: "deploy / uninstall / preview" },
  { key: "git_ref", detail: "the git ref (git sources)" },
  { key: "git_sha", detail: "resolved commit SHA (inline values)" },
  { key: "git_sha_short", detail: "short commit SHA (inline values)" },
];

// The three namespaces, the first thing suggested inside an empty `${{ }}`.
const NAMESPACES: { name: string; detail: string }[] = [
  { name: "vars", detail: "an application or component variable" },
  { name: "run", detail: "run context (id, action, git…)" },
  { name: "components", detail: "an upstream OpenTofu output" },
];

const OUTPUTS_SEP = ".outputs.";

// A suggestion row. `replacement` is the new dotted expression (without the
// surrounding `${{ }}`); applySuggestion wraps it. `terminal` marks a complete
// reference (it closes with ` }}` and ends the popup); a non-terminal one (e.g.
// `vars.`) advances to the next stage. A `disabled` row is an informational hint
// (e.g. "no outputs known yet") that can't be selected.
export interface Suggestion {
  replacement: string;
  label: string;
  detail?: string;
  terminal?: boolean;
  disabled?: boolean;
}

// An open `${{` the caret currently sits inside: its start index (at the `$`)
// and the inner text typed so far (between `${{` and the caret).
export interface ActiveRef {
  start: number;
  inner: string;
}

// findActiveRef returns the unterminated `${{` reference the caret is inside, or
// null when the caret isn't in one. "Inside" means: the nearest unescaped `${{`
// before the caret has no `}}` between it and the caret. An escaped `$${{` is not
// an opener.
export function findActiveRef(text: string, caret: number): ActiveRef | null {
  const before = text.slice(0, caret);
  const lastClose = before.lastIndexOf("}}");
  let open = -1;
  let from = before.length;
  for (;;) {
    const idx = before.lastIndexOf("${{", from);
    if (idx < 0) break;
    // `$${{` — this `${{` is the tail of an escape, not a real opener: keep
    // searching strictly before it.
    if (idx > 0 && before[idx - 1] === "$") {
      from = idx - 1;
      continue;
    }
    open = idx;
    break;
  }
  if (open < 0) return null;
  // A `}}` after the opener (but before the caret) means the ref already closed.
  if (lastClose > open) return null;
  return { start: open, inner: before.slice(open + 3) };
}

// limit keeps the popup short; prefix matching usually narrows it well below.
const MAX = 8;

// suggestionsFor returns the completion rows for the inner text of an active
// reference. It degrades gracefully: an upstream component with no known output
// keys still offers the component (its name + `.outputs.`) and, at the key stage,
// a hint that keys appear after the first successful run.
export function suggestionsFor(inner: string, ctx: RefContext): Suggestion[] {
  // Only the leading whitespace after `${{` is cosmetic; names may contain
  // spaces (component display names), so don't trim internally.
  const expr = inner.replace(/^\s+/, "");

  if (expr.startsWith("vars.")) {
    const prefix = expr.slice("vars.".length);
    const matches = ctx.varsNames
      .filter((n) => n.startsWith(prefix))
      .slice(0, MAX)
      .map<Suggestion>((name) => ({
        replacement: `vars.${name}`,
        label: name,
        terminal: true,
      }));
    if (matches.length === 0 && ctx.varsNames.length === 0) {
      return [hint("No variables defined yet")];
    }
    return matches;
  }

  if (expr.startsWith("run.")) {
    const prefix = expr.slice("run.".length);
    return RUN_KEYS.filter((k) => k.key.startsWith(prefix)).map<Suggestion>((k) => ({
      replacement: `run.${k.key}`,
      label: k.key,
      detail: k.detail,
      terminal: true,
    }));
  }

  if (expr.startsWith("components.")) {
    const rest = expr.slice("components.".length);
    const sepAt = rest.lastIndexOf(OUTPUTS_SEP);
    if (sepAt < 0) {
      // Still choosing the component name. Picking one jumps straight to its
      // `.outputs.` so the author doesn't have to type the separator.
      const matches = ctx.componentNames
        .filter((n) => n.startsWith(rest))
        .slice(0, MAX)
        .map<Suggestion>((name) => ({
          replacement: `components.${name}${OUTPUTS_SEP}`,
          label: name,
          detail: "outputs",
        }));
      if (matches.length === 0 && ctx.componentNames.length === 0) {
        return [hint("Add an OpenTofu dependency to reference its outputs")];
      }
      return matches;
    }
    // Choosing the output key for a named component.
    const name = rest.slice(0, sepAt);
    const keyPrefix = rest.slice(sepAt + OUTPUTS_SEP.length);
    const known = ctx.outputKeysByName[name];
    if (!known || known.length === 0) {
      return [hint("Outputs appear after this component's first successful run")];
    }
    return known
      .filter((k) => k.key.startsWith(keyPrefix))
      .slice(0, MAX)
      .map<Suggestion>((k) => ({
        replacement: `components.${name}${OUTPUTS_SEP}${k.key}`,
        label: k.key,
        detail: k.sensitive ? "sensitive" : k.type,
        terminal: true,
      }));
  }

  // Namespace stage (empty or a partial namespace, no recognized prefix yet).
  if (!expr.includes(".")) {
    return NAMESPACES.filter((n) => n.name.startsWith(expr)).map<Suggestion>((n) => ({
      replacement: `${n.name}.`,
      label: n.name,
      detail: n.detail,
    }));
  }
  return [];
}

// hint builds a non-selectable informational row.
function hint(label: string): Suggestion {
  return { replacement: "", label, disabled: true };
}

// applySuggestion splices a selected suggestion into the field, returning the new
// text and caret. It rewrites only `${{` … caret (never text after the caret),
// normalizing to `${{ <expr>`; a terminal suggestion appends ` }}` (absorbing a
// closing brace the user may have already typed) and a non-terminal one leaves
// the caret ready for the next stage.
export function applySuggestion(
  text: string,
  start: number,
  caret: number,
  s: Suggestion,
): { text: string; caret: number } {
  const head = text.slice(0, start);
  let tail = text.slice(caret);
  let insert = `\${{ ${s.replacement}`;
  if (s.terminal) {
    const m = tail.match(/^\s*\}\}/);
    if (m) tail = tail.slice(m[0].length);
    insert += " }}";
  }
  return { text: head + insert + tail, caret: head.length + insert.length };
}
