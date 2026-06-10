// Supported OpenTofu release lines, newest first — keep in lockstep with
// lib/tofu/versions.go (the registry the server validates tofu_version
// against; the SPA ships embedded in the same binary, so the two lists cannot
// skew in production). nativeLock marks lines whose s3 backend locks state
// natively in the bucket itself (use_lockfile, OpenTofu 1.10+) — for those the
// run turns locking on automatically and no DynamoDB table is needed.
export const TOFU_VERSIONS = [
  { minor: "1.12", nativeLock: true },
  { minor: "1.11", nativeLock: true },
  { minor: "1.10", nativeLock: true },
  { minor: "1.9", nativeLock: false },
] as const;

// The line a component with no tofu_version config runs (the server default,
// kept for components authored before the key existed).
export const TOFU_DEFAULT_VERSION = "1.9";

// The line new components are seeded with — the newest supported.
export const TOFU_SEED_VERSION = TOFU_VERSIONS[0].minor;

// tofuNativeLock reports whether the given tofu_version config value (possibly
// empty = the default line) locks s3 state natively.
export function tofuNativeLock(version: string | undefined): boolean {
  const minor = version || TOFU_DEFAULT_VERSION;
  return TOFU_VERSIONS.find((v) => v.minor === minor)?.nativeLock ?? false;
}
