package tofu

// Version describes one supported OpenTofu minor release line a terraform
// component can run (the component's tofu_version config key). Each line pins
// an exact patch image so runs are deterministic; bumping a patch is a
// one-line change here.
//
// Image facts (verified 2026-06: pulled and ran the tags): the standard
// ghcr.io/opentofu/opentofu tags are Alpine-based and bundle git/bash/openssh
// on every line through 1.12 — the step needs git to clone the root module.
// What changed at 1.10 is upstream *support*, not contents: the OpenTofu docs
// declare direct use of the official image unsupported (they won't
// security-refresh the Alpine base weekly) and base-image builds are blocked
// with an ONBUILD exit 1 — which does not affect running the image. The
// `-minimal` variants are FROM scratch (tofu binary only, no shell) and
// unusable for our script. If upstream ever stops publishing standard tags,
// the fallback is a first-party multi-stage image (COPY --from=
// opentofu:<ver>-minimal onto alpine + apk add git) repointed here.
type Version struct {
	// Minor is the release line as stored in the component's tofu_version
	// config (e.g. "1.12").
	Minor string
	// Image is the pinned CLI image the step runs in (exact patch tag).
	Image string
	// NativeS3Lock reports whether this line's s3 backend supports native
	// lockfile locking (`use_lockfile`, introduced in OpenTofu 1.10): the lock
	// is a conditional PutObject of `<state key>.tflock` in the state bucket,
	// so locking needs no extra infrastructure. The planner turns it on
	// automatically for these lines; older lines lock via a DynamoDB table.
	NativeS3Lock bool
}

// Versions is the supported OpenTofu lines, newest first (the UI mirrors this
// list and order in ComponentFields.tsx — keep them in lockstep; the SPA is
// embedded in the same binary, so they cannot skew in production).
var Versions = []Version{
	{Minor: "1.12", Image: "ghcr.io/opentofu/opentofu:1.12.1", NativeS3Lock: true},
	{Minor: "1.11", Image: "ghcr.io/opentofu/opentofu:1.11.8", NativeS3Lock: true},
	{Minor: "1.10", Image: "ghcr.io/opentofu/opentofu:1.10.10", NativeS3Lock: true},
	{Minor: "1.9", Image: "ghcr.io/opentofu/opentofu:1.9.4", NativeS3Lock: false},
}

// DefaultVersion is the line a component with no tofu_version config runs:
// the line components ran before the key existed, so pre-existing components
// keep their behavior (only patch-level drift). New components are seeded
// with the newest line by the UI; this default is deliberately NOT the newest
// — silently jumping existing state across minors would make any rollback
// best-effort (state downgrades are not guaranteed across minors).
const DefaultVersion = "1.9"

// ResolveVersion maps a component's tofu_version config value to its Version.
// Empty resolves to DefaultVersion. Unknown values report ok=false — write
// time validation rejects them, so a planner hitting one means a stale row
// from before that validation; callers fail loudly rather than guessing.
func ResolveVersion(minor string) (Version, bool) {
	if minor == "" {
		minor = DefaultVersion
	}
	for _, v := range Versions {
		if v.Minor == minor {
			return v, true
		}
	}
	return Version{}, false
}
