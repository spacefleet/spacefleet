// The helm component's `values_sources` config key is a JSON-encoded string of
// an ordered list of git value sources (see the planner). The canvas edits them
// as a structured list and serializes back to that single string key on save.

export interface ValuesSourceRow {
  repo_url: string;
  git_ref?: string;
  path: string;
}

// parseValuesSources reads the JSON string stored under config.values_sources.
// It's defensive: an empty/missing/unparseable value yields an empty list so a
// hand-broken value never crashes the editor.
export function parseValuesSources(raw: string | undefined): ValuesSourceRow[] {
  if (!raw || raw.trim() === "") return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((s): s is Record<string, unknown> => typeof s === "object" && s !== null)
      .map((s) => ({
        repo_url: typeof s.repo_url === "string" ? s.repo_url : "",
        git_ref: typeof s.git_ref === "string" ? s.git_ref : undefined,
        path: typeof s.path === "string" ? s.path : "",
      }));
  } catch {
    return [];
  }
}

// serializeValuesSources produces the JSON string for config.values_sources, or
// "" when there are no usable rows (a row needs a repo url). Empty refs are
// omitted; values are trimmed. Returning "" lets the caller drop the key.
export function serializeValuesSources(rows: ValuesSourceRow[]): string {
  const clean = rows
    .filter((s) => s.repo_url.trim() !== "")
    .map((s) => ({
      repo_url: s.repo_url.trim(),
      path: (s.path ?? "").trim(),
      ...(s.git_ref?.trim() ? { git_ref: s.git_ref.trim() } : {}),
    }));
  if (clean.length === 0) return "";
  return JSON.stringify(clean);
}
