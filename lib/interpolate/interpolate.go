// Package interpolate implements the `${{ <namespace>.<path> }}` reference
// syntax usable in component configuration (GitHub-Actions style, chosen so it
// collides with neither Helm's `{{ }}` nor shell `$VAR`). Three namespaces
// exist: `vars.<NAME>` (the component's merged variables), `run.<key>` (run
// context), and `components.<name>.outputs.<key>` (upstream component
// outputs). The package is pure and dependency-free: Parse splits a string
// into literal/reference segments, Refs exposes the references for save-time
// validation, and Render substitutes them through a caller-supplied lookup —
// what each reference resolves to (and which are allowed where) is entirely
// the caller's policy.
package interpolate

import (
	"fmt"
	"regexp"
	"strings"
)

// The reference namespaces. Anything else is a parse error.
const (
	NamespaceVars       = "vars"
	NamespaceRun        = "run"
	NamespaceComponents = "components"
)

// identRe is the shape of a vars name or run key: a letter or underscore
// followed by letters, digits, or underscores — the same rule lib/variables
// enforces on variable names, so every storable variable is referenceable.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// outputsSep is the fixed separator between a component name and its output
// key in a components reference. Component names may themselves contain dots,
// so parseExpr splits on the LAST occurrence.
const outputsSep = ".outputs."

// Ref is one parsed `${{ ... }}` reference. Namespace selects which of the
// other fields carry the path: Var for vars, RunKey for run, Component+Output
// for components. Raw is the authored text including the braces, for error
// messages that name the exact reference.
type Ref struct {
	Namespace string
	Var       string
	RunKey    string
	Component string
	Output    string
	Raw       string
}

// segment is one piece of a parsed template: a literal run of text (ref nil)
// or a single reference.
type segment struct {
	literal string
	ref     *Ref
}

// Template is a parsed string: an alternating sequence of literals and
// references, ready to render any number of times.
type Template struct {
	segments []segment
}

// Parse splits s into literal and reference segments. `$${{` escapes to a
// literal `${{`; any other `${{` must close with `}}` and carry a well-formed
// expression, or Parse errors (better loud than silently literal).
func Parse(s string) (Template, error) {
	var t Template
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t.segments = append(t.segments, segment{literal: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "$${{") {
			lit.WriteString("${{")
			i += len("$${{")
			continue
		}
		if strings.HasPrefix(s[i:], "${{") {
			end := strings.Index(s[i:], "}}")
			if end < 0 {
				return Template{}, fmt.Errorf("unterminated ${{ reference (missing }}; write $${{ for a literal ${{)")
			}
			raw := s[i : i+end+len("}}")]
			ref, err := parseExpr(strings.TrimSpace(s[i+len("${{"):i+end]), raw)
			if err != nil {
				return Template{}, err
			}
			flush()
			t.segments = append(t.segments, segment{ref: &ref})
			i += end + len("}}")
			continue
		}
		lit.WriteByte(s[i])
		i++
	}
	flush()
	return t, nil
}

// parseExpr parses the dot-separated path inside one reference's braces (outer
// whitespace already trimmed). raw is the authored reference for error
// messages.
func parseExpr(expr, raw string) (Ref, error) {
	if expr == "" {
		return Ref{}, fmt.Errorf("%s: empty reference (expected %s.<NAME>, %s.<key>, or %s.<name>%s<key>)", raw, NamespaceVars, NamespaceRun, NamespaceComponents, outputsSep)
	}
	ns, rest, _ := strings.Cut(expr, ".")
	switch ns {
	case NamespaceVars:
		if !identRe.MatchString(rest) {
			return Ref{}, fmt.Errorf("%s: vars takes exactly one variable name of letters, digits, and underscores (e.g. vars.MY_VAR)", raw)
		}
		return Ref{Namespace: ns, Var: rest, Raw: raw}, nil
	case NamespaceRun:
		if !identRe.MatchString(rest) {
			return Ref{}, fmt.Errorf("%s: run takes exactly one key (e.g. run.id)", raw)
		}
		return Ref{Namespace: ns, RunKey: rest, Raw: raw}, nil
	case NamespaceComponents:
		// The component name may contain dots (it is a free-form display name),
		// so split on the last ".outputs." — existence of the named component is
		// the caller's validation, not a parse concern.
		idx := strings.LastIndex(rest, outputsSep)
		if idx < 0 {
			return Ref{}, fmt.Errorf("%s: components references take the form components.<name>%s<key>", raw, outputsSep)
		}
		name, key := rest[:idx], rest[idx+len(outputsSep):]
		if name == "" || key == "" {
			return Ref{}, fmt.Errorf("%s: components references take the form components.<name>%s<key>", raw, outputsSep)
		}
		return Ref{Namespace: ns, Component: name, Output: key, Raw: raw}, nil
	default:
		return Ref{}, fmt.Errorf("%s: unknown namespace %q (supported: %s, %s, %s)", raw, ns, NamespaceVars, NamespaceRun, NamespaceComponents)
	}
}

// Refs returns every reference in the template, in order of appearance, for
// save-time validation.
func (t Template) Refs() []Ref {
	var refs []Ref
	for _, seg := range t.segments {
		if seg.ref != nil {
			refs = append(refs, *seg.ref)
		}
	}
	return refs
}

// Render substitutes each reference via lookup and returns the assembled
// string. The first lookup error aborts the render and is returned as-is — the
// lookup owns naming the failing reference (Ref.Raw), so its message can land
// directly in a component_run failure message.
func (t Template) Render(lookup func(Ref) (string, error)) (string, error) {
	var b strings.Builder
	for _, seg := range t.segments {
		if seg.ref == nil {
			b.WriteString(seg.literal)
			continue
		}
		v, err := lookup(*seg.ref)
		if err != nil {
			return "", err
		}
		b.WriteString(v)
	}
	return b.String(), nil
}
