// slugify corrects typed text toward a slug (lowercase letters, digits, and
// hyphens) as the user types: letters are lowercased, spaces and underscores
// become a hyphen, runs of hyphens collapse, a leading hyphen is dropped, and
// any other character is ignored. A trailing hyphen is kept — the user is
// mid-word — and trimmed on blur.
export function slugify(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-{2,}/g, "-")
    .replace(/^-+/, "");
}
