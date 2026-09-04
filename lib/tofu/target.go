package tofu

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Targeted runs: a component-scoped deploy or destroy may be narrowed to a
// fixed list of resource addresses, rendered as `-target=<address>` plan
// flags. The list is typed input — validated addresses, never free-form
// flags — so a run can only ever add targets, not arbitrary CLI arguments.

// ErrInvalidTarget is the sentinel every ValidateTargets failure wraps, so a
// handler can map any of them to a 400.
var ErrInvalidTarget = errors.New("tofu: invalid target address")

// Bounds on a target list: a handful of addresses, each a short token.
const (
	maxTargets   = 50
	maxTargetLen = 1024
)

// targetAddressRe is the shape of a resource address: dot-separated segments
// (a resource type and name, `module.<name>` prefixes, a `data.` prefix),
// each optionally indexed by an integer or a double-quoted key
// (`aws_instance.web[0]`, `module.vpc["prod"].aws_subnet.private`).
var targetAddressRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\[(\d+|"[^"\\]*")\])?(\.[A-Za-z_][A-Za-z0-9_-]*(\[(\d+|"[^"\\]*")\])?)*$`)

// ValidateTargets checks a target list: at most maxTargets distinct
// addresses, each a well-formed resource or module address (see
// targetAddressRe) of at least two segments — a bare `aws_instance` is a
// type, not an address. An empty list is valid (no targeting). The script
// shell-quotes every flag regardless; this is the user-facing check.
func ValidateTargets(targets []string) error {
	if len(targets) > maxTargets {
		return fmt.Errorf("%w: at most %d targets", ErrInvalidTarget, maxTargets)
	}
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		switch {
		case t == "":
			return fmt.Errorf("%w: a target must not be empty", ErrInvalidTarget)
		case len(t) > maxTargetLen:
			return fmt.Errorf("%w: %q is too long (at most %d characters)", ErrInvalidTarget, t, maxTargetLen)
		case !targetAddressRe.MatchString(t) || !strings.Contains(t, "."):
			return fmt.Errorf("%w: %q is not a resource address (e.g. aws_instance.web or module.vpc.aws_subnet.private[0])", ErrInvalidTarget, t)
		}
		if _, dup := seen[t]; dup {
			return fmt.Errorf("%w: %q is listed twice", ErrInvalidTarget, t)
		}
		seen[t] = struct{}{}
	}
	return nil
}

// TargetFlags renders a validated target list as the `-target=<address>`
// plan flags (one element per target, in order) to append to a component's
// plan flags. nil for an empty list.
func TargetFlags(targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, "-target="+t)
	}
	return out
}
