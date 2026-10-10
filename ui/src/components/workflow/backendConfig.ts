// parseBackendConfig defensively parses the JSON-object string the terraform
// node stores in config.backend_config into a flat string map. A
// missing/invalid value yields an empty object (the fields start blank).
export function parseBackendConfig(raw: string | undefined): Record<string, string> {
  if (!raw || raw.trim() === "") return {};
  try {
    const obj = JSON.parse(raw) as unknown;
    if (!obj || typeof obj !== "object" || Array.isArray(obj)) return {};
    return Object.fromEntries(
      Object.entries(obj as Record<string, unknown>).map(([key, value]) => [
        key,
        value == null ? "" : String(value),
      ]),
    );
  } catch {
    return {};
  }
}
