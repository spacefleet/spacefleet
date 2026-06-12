package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/interpolate"
)

// The run.* keys a ${{ run.<key> }} reference may name. id/action/git_ref are
// resolved server-side at plan time; git_sha/git_sha_short are resolved
// in-script after the chart clone (the SHA comes from the very clone being
// deployed), so they are only available in the inline values of a git-source
// helm component — the namespace and release name are needed before the clone
// happens.
const (
	runKeyID          = "id"
	runKeyAction      = "action"
	runKeyGitRef      = "git_ref"
	runKeyGitSHA      = "git_sha"
	runKeyGitSHAShort = "git_sha_short"
)

// supportedRunKeys renders the run.* key set for error messages.
const supportedRunKeys = runKeyID + ", " + runKeyAction + ", " + runKeyGitRef + ", " + runKeyGitSHA + ", " + runKeyGitSHAShort

// Names of the interpolable helm fields, shared by plan-time render errors and
// save-time validation errors so a user sees the same vocabulary in both.
const (
	helmFieldValues      = helmConfigValues
	helmFieldReleaseName = helmConfigReleaseName
	helmFieldNamespace   = "target_namespace"
)

// outputsLookup resolves an authored component name to its parsed recorded
// outputs for the ${{ components.* }} render context. The error it returns is
// user-facing (the renderer prefixes it with the failing reference and it
// lands verbatim in the component_run failure message), so it names the
// component and what to do about it.
type outputsLookup func(name string) (map[string]tofuOutput, error)

// helmRenderer renders one helm node's interpolation references at plan time.
// vars is the merged variable env (non-secret overlaid by sensitive — the same
// set the step gets as env; the rendered strings land only in the per-run
// mounted-files Secret, which already carries the kubeconfig and chart
// credentials, so this leaks nothing new). outputs resolves the
// components.<name>.outputs.* namespace (see WorkflowRunWorker.outputsLookup).
// gitContext flips when any run.git_sha* reference rendered (to its sentinel),
// telling helm.Script to substitute the real SHAs after the clone.
type helmRenderer struct {
	node    GraphNode
	action  string
	runID   uuid.UUID
	vars    map[string]string
	outputs outputsLookup

	gitContext bool
}

// render parses and renders one field. field names the field in error messages;
// inValues gates the run.git_sha* keys. The second return reports whether the
// field contained any references (the namespace post-validation only applies to
// a rendered namespace). Errors are phrased to be actionable as a
// component_run failure message — runComponent persists them verbatim.
func (r *helmRenderer) render(field, in string, inValues bool) (string, bool, error) {
	tpl, err := interpolate.Parse(in)
	if err != nil {
		return "", false, fmt.Errorf("workflows: component %q %s: %w", r.node.Name, field, err)
	}
	out, err := tpl.Render(r.lookup(inValues))
	if err != nil {
		return "", false, fmt.Errorf("workflows: component %q %s: %w", r.node.Name, field, err)
	}
	return out, len(tpl.Refs()) > 0, nil
}

// lookup resolves one reference. Unknown variables and misplaced run keys are
// plan-time failures (save-time validation catches the statically knowable
// ones; variables are mutable independently of the workflow, so vars.* is only
// checkable here).
func (r *helmRenderer) lookup(inValues bool) func(interpolate.Ref) (string, error) {
	gitSource := r.node.Config[helmConfigChartSource] == helm.SourceGit
	return func(ref interpolate.Ref) (string, error) {
		switch ref.Namespace {
		case interpolate.NamespaceVars:
			v, ok := r.vars[ref.Var]
			if !ok {
				return "", fmt.Errorf("%s: variable %q is not defined for this component — define it on the application or component", ref.Raw, ref.Var)
			}
			return v, nil
		case interpolate.NamespaceRun:
			switch ref.RunKey {
			case runKeyID:
				return r.runID.String(), nil
			case runKeyAction:
				return r.action, nil
			case runKeyGitRef:
				v := r.node.Config[helm.ConfigGitRef]
				if !gitSource || v == "" {
					return "", fmt.Errorf("%s: run.git_ref requires a git chart source with an explicit git_ref", ref.Raw)
				}
				return v, nil
			case runKeyGitSHA, runKeyGitSHAShort:
				if !gitSource {
					return "", fmt.Errorf("%s: run.%s requires a git chart source", ref.Raw, ref.RunKey)
				}
				if !inValues {
					return "", fmt.Errorf("%s: run.%s is only available in the inline values", ref.Raw, ref.RunKey)
				}
				r.gitContext = true
				if ref.RunKey == runKeyGitSHA {
					return helm.GitSHASentinel, nil
				}
				return helm.GitSHAShortSentinel, nil
			default:
				return "", fmt.Errorf("%s: unknown run key %q (supported: %s)", ref.Raw, ref.RunKey, supportedRunKeys)
			}
		case interpolate.NamespaceComponents:
			if r.outputs == nil {
				return "", fmt.Errorf("%s: components.* references are not available in this context", ref.Raw)
			}
			outs, err := r.outputs(ref.Component)
			if err != nil {
				return "", fmt.Errorf("%s: %w", ref.Raw, err)
			}
			out, ok := outs[ref.Output]
			if !ok {
				return "", fmt.Errorf("%s: component %q has no recorded output %q — check the output name, and re-deploy the component if it was added recently", ref.Raw, ref.Component, ref.Output)
			}
			return renderOutputValue(out.Value), nil
		default:
			return "", fmt.Errorf("%s: unknown namespace %q", ref.Raw, ref.Namespace)
		}
	}
}

// renderOutputValue renders one recorded tofu output value into the
// interpolated string: a JSON string renders bare (the common case — a
// namespace, a hostname); any other type (number, bool, list, object) renders
// as its compact JSON encoding.
func renderOutputValue(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return string(v)
	}
	return buf.String()
}

// outputsLookup builds the components.* resolution for one node's render pass:
// authored name → the recorded outputs of that component's apply unit,
// resolved this-run-first then latest-successful (w.resolveOutputs), cached so
// several references to one component cost one query. byID is the run
// snapshot's execution units — the only authoritative name→id mapping for this
// run.
func (w *WorkflowRunWorker) outputsLookup(ctx context.Context, orgID, runID uuid.UUID, byID map[uuid.UUID]GraphNode) outputsLookup {
	cache := make(map[string]map[string]tofuOutput)
	return func(name string) (map[string]tofuOutput, error) {
		if outs, ok := cache[name]; ok {
			return outs, nil
		}
		applyID, err := tofuApplyUnitID(name, byID)
		if err != nil {
			return nil, err
		}
		raw, err := w.resolveOutputs(ctx, orgID, runID, applyID)
		if err != nil {
			return nil, fmt.Errorf("read the recorded outputs of component %q: %w", name, err)
		}
		if raw == "" {
			return nil, fmt.Errorf("component %q has no recorded outputs — deploy it successfully first", name)
		}
		var outs map[string]tofuOutput
		if err := json.Unmarshal([]byte(raw), &outs); err != nil {
			return nil, fmt.Errorf("the recorded outputs of component %q are unreadable: %v", name, err)
		}
		cache[name] = outs
		return outs, nil
	}
}

// tofuApplyUnitID resolves an authored component name to the id of its apply
// execution unit within the run snapshot — the key its outputs rows are stored
// under (the apply unit's id IS deriveApplyID(authored id), assigned by
// expandExecutionNodes). The authored name is recovered by trimming the
// expansion's display suffix. Save-time validation guarantees a reference
// names exactly one terraform component, but the snapshot may predate that
// validation (or carry duplicate names), so both failure modes keep
// actionable messages.
func tofuApplyUnitID(name string, byID map[uuid.UUID]GraphNode) (uuid.UUID, error) {
	var id uuid.UUID
	found := false
	for _, n := range byID {
		if n.Type != TypeTerraform || n.Config[terraformConfigCommand] != terraformCommandApply {
			continue
		}
		if strings.TrimSuffix(n.Name, tofuApplyNameSuffix) != name {
			continue
		}
		if found {
			return uuid.Nil, fmt.Errorf("component name %q is ambiguous in this run — rename one of the OpenTofu components", name)
		}
		id, found = n.ID, true
	}
	if !found {
		return uuid.Nil, fmt.Errorf("component %q is not an OpenTofu component of this run — outputs can only be referenced from an upstream OpenTofu component", name)
	}
	return id, nil
}

// mergeVars merges the resolver's non-secret and sensitive variable env into
// the single vars.* lookup context (the maps are disjoint by the resolver's
// construction; secret is overlaid defensively).
func mergeVars(plain, secret map[string]string) map[string]string {
	out := make(map[string]string, len(plain)+len(secret))
	maps.Copy(out, plain)
	maps.Copy(out, secret)
	return out
}

// dnsLabelRe is a DNS-1123 label: lowercase alphanumerics and '-', starting and
// ending alphanumeric.
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validDNSLabel reports whether s is a valid DNS-1123 label (the shape of a
// Kubernetes namespace name).
func validDNSLabel(s string) bool {
	return len(s) <= 63 && dnsLabelRe.MatchString(s)
}

// validateHelmInterpolation rejects, at save time, every ${{ }} reference in a
// helm node's inline values, release name, or target namespace that could
// never render: malformed syntax, unknown run.* keys, run.git_* on a non-git
// chart source, run.git_sha* outside the inline values (the SHA comes from the
// chart clone, which hasn't happened when the namespace/release name are
// needed), and run.git_ref without an explicit git_ref (the default-branch
// name isn't known server-side). vars.* names are deliberately NOT checked
// against the stored variables — variables are mutable independently of the
// workflow, so a save-time check would go stale; a missing one fails the node
// at plan time with a clear message. components.* references need the whole
// graph (name resolution + ancestry), so they're checked by the workflow-level
// validateOutputRefs, not here. Failures wrap ErrInvalidConfig so a handler
// maps them to a 400.
func validateHelmInterpolation(n ComponentInput) error {
	gitSource := n.Config[helmConfigChartSource] == helm.SourceGit
	for _, f := range helmInterpolationFields(n) {
		tpl, err := interpolate.Parse(f.value)
		if err != nil {
			return fmt.Errorf("%w: node %q (helm) %s: %v", ErrInvalidConfig, n.Name, f.name, err)
		}
		for _, ref := range tpl.Refs() {
			if err := validateHelmRef(n, ref, f.name, f.inValues, gitSource); err != nil {
				return err
			}
		}
	}
	return nil
}

// helmField is one interpolable helm field for validation: its user-facing
// name, its authored value, and whether it is the inline values (the only
// place run.git_sha* may appear).
type helmField struct {
	name     string
	value    string
	inValues bool
}

// helmInterpolationFields lists a helm node's interpolable fields — the same
// three the planner renders — shared by the per-node reference validation and
// the workflow-level components.* check so neither can drift from the render
// pass.
func helmInterpolationFields(n ComponentInput) []helmField {
	return []helmField{
		{helmFieldValues, n.Config[helmConfigValues], true},
		{helmFieldReleaseName, n.Config[helmConfigReleaseName], false},
		{helmFieldNamespace, n.TargetNamespace, false},
	}
}

// validateOutputRefs checks every ${{ components.<name>.outputs.<key> }}
// reference across the workflow's helm nodes against the authored graph: the
// name must resolve to exactly one component (a duplicated name is only an
// error when referenced), that component must be an OpenTofu (terraform) one,
// and it must be a transitive depends_on ancestor of the referencing node —
// the DAG already guarantees an ancestor settles before the referencing node
// plans, so checking ancestry here surfaces the mistake at authoring time
// instead of mid-run. expanded is the component-level adjacency from
// expandDependencies (groups desugared) — the same edges the run executes, so
// a dependency routed through a group counts. Output keys are run-time data
// and deliberately not validated (the parser already rejects an empty key).
// Parse errors are skipped — validateHelmInterpolation rejected them first.
// Failures wrap ErrInvalidConfig so a handler maps them to a 400.
func validateOutputRefs(components []ComponentInput, expanded map[uuid.UUID][]uuid.UUID) error {
	byName := make(map[string][]ComponentInput, len(components))
	for _, c := range components {
		byName[c.Name] = append(byName[c.Name], c)
	}
	for _, n := range components {
		if n.Type != TypeHelm {
			continue
		}
		// The ancestor closure is computed lazily — most nodes carry no
		// components references.
		var ancestors map[uuid.UUID]struct{}
		for _, f := range helmInterpolationFields(n) {
			tpl, err := interpolate.Parse(f.value)
			if err != nil {
				continue
			}
			for _, ref := range tpl.Refs() {
				if ref.Namespace != interpolate.NamespaceComponents {
					continue
				}
				target := byName[ref.Component]
				if len(target) == 0 {
					return fmt.Errorf("%w: node %q (helm) %s: %s: no component named %q in this workflow", ErrInvalidConfig, n.Name, f.name, ref.Raw, ref.Component)
				}
				if len(target) > 1 {
					return fmt.Errorf("%w: node %q (helm) %s: %s: component name %q is ambiguous — rename one of the components", ErrInvalidConfig, n.Name, f.name, ref.Raw, ref.Component)
				}
				if target[0].Type != TypeTerraform {
					return fmt.Errorf("%w: node %q (helm) %s: %s: component %q is a %s component — only OpenTofu (terraform) components publish outputs", ErrInvalidConfig, n.Name, f.name, ref.Raw, ref.Component, target[0].Type)
				}
				if ancestors == nil {
					ancestors = transitiveDeps(expanded, n.ID)
				}
				if _, ok := ancestors[target[0].ID]; !ok {
					return fmt.Errorf("%w: node %q (helm) %s: %s: component %q must be an upstream dependency of %q — add it to the depends_on chain", ErrInvalidConfig, n.Name, f.name, ref.Raw, ref.Component, n.Name)
				}
			}
		}
	}
	return nil
}

// transitiveDeps returns the transitive depends_on closure of one node (every
// ancestor id) over the expanded component-level adjacency. The graph is
// already cycle-checked, but the seen set makes the walk safe regardless.
func transitiveDeps(expanded map[uuid.UUID][]uuid.UUID, id uuid.UUID) map[uuid.UUID]struct{} {
	seen := make(map[uuid.UUID]struct{})
	stack := append([]uuid.UUID(nil), expanded[id]...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		stack = append(stack, expanded[d]...)
	}
	return seen
}

// validateHelmRef checks one reference against the field it appears in. vars
// references are shape-checked by the parser and resolved at plan time, and
// components references are validated against the whole graph in
// validateOutputRefs, so only run.* placement is rejected here.
func validateHelmRef(n ComponentInput, ref interpolate.Ref, field string, inValues, gitSource bool) error {
	switch ref.Namespace {
	case interpolate.NamespaceVars, interpolate.NamespaceComponents:
		return nil
	case interpolate.NamespaceRun:
		switch ref.RunKey {
		case runKeyID, runKeyAction:
			return nil
		case runKeyGitRef:
			if !gitSource {
				return fmt.Errorf("%w: node %q (helm) %s: %s requires a git chart source", ErrInvalidConfig, n.Name, field, ref.Raw)
			}
			if n.Config[helm.ConfigGitRef] == "" {
				return fmt.Errorf("%w: node %q (helm) %s: %s requires an explicit git_ref on the component", ErrInvalidConfig, n.Name, field, ref.Raw)
			}
			return nil
		case runKeyGitSHA, runKeyGitSHAShort:
			if !gitSource {
				return fmt.Errorf("%w: node %q (helm) %s: %s requires a git chart source", ErrInvalidConfig, n.Name, field, ref.Raw)
			}
			if !inValues {
				return fmt.Errorf("%w: node %q (helm) %s: %s is only available in the inline values", ErrInvalidConfig, n.Name, field, ref.Raw)
			}
			return nil
		default:
			return fmt.Errorf("%w: node %q (helm) %s: %s: unknown run key %q (supported: %s)", ErrInvalidConfig, n.Name, field, ref.Raw, ref.RunKey, supportedRunKeys)
		}
	}
	// Unreachable: the parser only yields the three namespaces.
	return nil
}
