// Package policy evaluates Rego policies against an OpenTofu plan: the
// policy-as-code gate between a plan and its apply. A policy is a Rego
// module in package `spacefleet` whose `deny` rule collects violation
// messages; Spacefleet evaluates every policy that applies to a component's
// plan and either blocks the apply (a `block` policy with violations) or
// records warnings (a `warn` policy). The engine is pure — it knows nothing
// about the database — and the input document it hands the policy is a
// small, documented JSON shape built from the parsed plan (never the plan
// text or attribute values, which can carry secrets).
package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Enforcement levels.
const (
	// EnforcementBlock fails the plan step (and so skips the apply) on any
	// violation.
	EnforcementBlock = "block"
	// EnforcementWarn records the violations for the approver to see and
	// lets the run proceed.
	EnforcementWarn = "warn"
)

// ValidEnforcement reports whether v is an enforcement level.
func ValidEnforcement(v string) bool {
	return v == EnforcementBlock || v == EnforcementWarn
}

// Package is the Rego package every policy must declare; the deny rule is
// read from data.<Package>.deny.
const Package = "spacefleet"

// evalTimeout bounds one policy's evaluation: a plan input is small, so a
// policy that takes longer is looping.
const evalTimeout = 5 * time.Second

// ErrInvalidPolicy is the sentinel every Compile failure wraps, so a
// handler can map any of them to a 400.
var ErrInvalidPolicy = errors.New("policy: invalid policy")

// Policy is one policy to evaluate: its identity for the verdict, its Rego
// source, and its enforcement level.
type Policy struct {
	ID          string
	Name        string
	Rego        string
	Enforcement string
}

// Input is the document a policy sees as `input`. The field names are the
// JSON names — they are the policy author's contract (see the user docs).
type Input struct {
	Application Ref       `json:"application"`
	Component   Ref       `json:"component"`
	Run         RunInput  `json:"run"`
	Plan        PlanInput `json:"plan"`
}

// Ref names an application or component.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RunInput is what a policy may know about the run.
type RunInput struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	StartedBy string `json:"started_by"`
}

// PlanInput is the parsed plan: counts and one record per planned change.
type PlanInput struct {
	HasChanges     bool             `json:"has_changes"`
	Add            int              `json:"add"`
	Change         int              `json:"change"`
	Destroy        int              `json:"destroy"`
	Replace        int              `json:"replace"`
	OutputsChanged bool             `json:"outputs_changed"`
	Resources      []ResourceChange `json:"resources"`
}

// ResourceChange is one planned change: the full address, its parts, the
// action (create / update / replace / delete / read / move / import /
// forget / other), and OpenTofu's one-line detail.
type ResourceChange struct {
	Address string `json:"address"`
	// Module is the module path (e.g. "module.vpc"), "" for the root module.
	Module string `json:"module"`
	// Mode is "managed" or "data".
	Mode   string `json:"mode"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// NewPlanInput builds the plan part of the input from a parsed plan.
func NewPlanInput(p tofu.Plan) PlanInput {
	out := PlanInput{
		HasChanges:     p.HasChanges,
		Add:            p.Add,
		Change:         p.Change,
		Destroy:        p.Destroy,
		Replace:        p.Replace,
		OutputsChanged: p.OutputsChanged,
		Resources:      make([]ResourceChange, 0, len(p.Resources)),
	}
	for _, r := range p.Resources {
		rc := ResourceChange{Address: r.Address, Action: r.Action, Detail: r.Detail}
		rc.Module, rc.Mode, rc.Type, rc.Name = ParseAddress(r.Address)
		out.Resources = append(out.Resources, rc)
	}
	return out
}

// addressIndex strips an instance key ([0], ["k"]) off an address segment.
var addressIndex = regexp.MustCompile(`\[[^\]]*\]$`)

// ParseAddress splits a resource address into its module path, mode, type,
// and name: `module.vpc["a"].aws_subnet.private[0]` → ("module.vpc[\"a\"]",
// "managed", "aws_subnet", "private"); `data.aws_ami.x` → ("", "data",
// "aws_ami", "x"). An address that does not end in type.name yields empty
// type/name.
func ParseAddress(addr string) (module, mode, typ, name string) {
	mode = "managed"
	rest := addr
	var modParts []string
	for strings.HasPrefix(rest, "module.") {
		// module.<name>[index].
		seg := rest[len("module."):]
		end := strings.IndexByte(seg, '.')
		// An index may itself contain a dot inside quotes; find the segment end
		// as the first '.' after any closing bracket.
		if i := strings.IndexByte(seg, '['); i >= 0 && (end < 0 || i < end) {
			if j := strings.IndexByte(seg[i:], ']'); j >= 0 {
				end = strings.IndexByte(seg[i+j:], '.')
				if end >= 0 {
					end += i + j
				}
			}
		}
		if end < 0 {
			modParts = append(modParts, "module."+seg)
			rest = ""
			break
		}
		modParts = append(modParts, "module."+seg[:end])
		rest = seg[end+1:]
	}
	module = strings.Join(modParts, ".")
	if strings.HasPrefix(rest, "data.") {
		mode = "data"
		rest = rest[len("data."):]
	}
	rest = addressIndex.ReplaceAllString(rest, "")
	if i := strings.IndexByte(rest, '.'); i > 0 {
		typ, name = rest[:i], rest[i+1:]
	}
	return module, mode, typ, name
}

// Compile checks a policy's Rego: it must parse, declare package
// spacefleet, and compile. Failures wrap ErrInvalidPolicy with OPA's
// message, which names the line.
func Compile(src string) error {
	mod, err := ast.ParseModule("policy.rego", src)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if got := mod.Package.Path.String(); got != "data."+Package {
		return fmt.Errorf("%w: the package must be %q (found %s)", ErrInvalidPolicy, Package, strings.TrimPrefix(got, "data."))
	}
	c := ast.NewCompiler()
	c.Compile(map[string]*ast.Module{"policy.rego": mod})
	if c.Failed() {
		return fmt.Errorf("%w: %v", ErrInvalidPolicy, c.Errors)
	}
	return nil
}

// Result is one policy's outcome against one plan.
type Result struct {
	PolicyID    string   `json:"policy_id"`
	PolicyName  string   `json:"policy_name"`
	Enforcement string   `json:"enforcement"`
	Violations  []string `json:"violations,omitempty"`
	// Error is set when the policy could not be evaluated (a runtime error
	// or a timeout). A block policy that errors blocks — fail closed.
	Error string `json:"error,omitempty"`
}

// Verdict is the evaluation of every applicable policy against one plan:
// stored on the plan step and shown at the approval gate. Blocked is true
// when any block policy had a violation or an error; Warned when any warn
// policy had one.
type Verdict struct {
	EvaluatedAt time.Time `json:"evaluated_at"`
	Results     []Result  `json:"results"`
	Blocked     bool      `json:"blocked"`
	Warned      bool      `json:"warned"`
}

// Evaluate runs every policy against the input. Policies are evaluated
// independently; one that fails to compile or errors at runtime records
// its error rather than aborting the rest.
func Evaluate(ctx context.Context, policies []Policy, in Input) Verdict {
	v := Verdict{EvaluatedAt: time.Now(), Results: make([]Result, 0, len(policies))}
	for _, p := range policies {
		r := Result{PolicyID: p.ID, PolicyName: p.Name, Enforcement: p.Enforcement}
		msgs, err := evalOne(ctx, p.Rego, in)
		if err != nil {
			r.Error = err.Error()
		}
		r.Violations = msgs
		if len(r.Violations) > 0 || r.Error != "" {
			switch p.Enforcement {
			case EnforcementBlock:
				v.Blocked = true
			default:
				v.Warned = true
			}
		}
		v.Results = append(v.Results, r)
	}
	return v
}

// evalOne evaluates data.spacefleet.deny of one policy and returns the
// messages it produced, sorted.
func evalOne(ctx context.Context, src string, in Input) ([]string, error) {
	if err := Compile(src); err != nil {
		return nil, fmt.Errorf("compile: %v", errors.Unwrap(err))
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	q, err := rego.New(
		rego.Query("data."+Package+".deny"),
		rego.Module("policy.rego", src),
		rego.Input(in),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile: %v", err)
	}
	rs, err := q.Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate: %v", err)
	}
	var msgs []string
	for _, r := range rs {
		for _, e := range r.Expressions {
			msgs = append(msgs, flatten(e.Value)...)
		}
	}
	sort.Strings(msgs)
	return msgs, nil
}

// flatten renders a deny rule's value as messages: a set/array of strings
// (each an entry), a single string, or anything else rendered with %v.
func flatten(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, flatten(e)...)
		}
		return out
	case string:
		return []string{t}
	case bool:
		if t {
			return []string{"denied"}
		}
		return nil
	default:
		return []string{fmt.Sprint(t)}
	}
}
