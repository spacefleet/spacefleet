// Package slug validates the human-supplied names that get reused verbatim as
// machine-facing identifiers — a Helm release name, a Kubernetes namespace, the
// key in a ${{ components.<name>.outputs.* }} reference. A name that is a valid
// slug is safe to embed in all of those without escaping or surprise, so the
// application and component layers gate their names through here.
package slug

import "regexp"

// MaxLen is the longest a slug may be. It matches the DNS-1123 label limit — the
// tightest of the downstream uses (a Kubernetes namespace) — so a valid slug is
// short enough to be one.
const MaxLen = 63

// Rule is a human-readable description of the slug format, phrased to follow the
// offending name in an error (`name %q <Rule>`).
const Rule = `must be a slug: lowercase letters, digits, and hyphens, starting and ending with a letter or digit (e.g. "my-app")`

// re is the DNS-1123 label shape: lowercase alphanumerics and interior hyphens,
// no leading or trailing hyphen.
var re = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Valid reports whether s is a valid slug: a non-empty, at-most-MaxLen string of
// lowercase letters, digits, and interior hyphens. This is exactly the rule a
// Kubernetes namespace and a Helm release name must satisfy, so a valid slug can
// be reused as either without further sanitizing.
func Valid(s string) bool {
	return len(s) > 0 && len(s) <= MaxLen && re.MatchString(s)
}
