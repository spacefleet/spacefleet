package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/workflowstage"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// RunScope narrows a deploy or uninstall run to one component — a
// per-component destroy or uninstall, or a deploy of just one OpenTofu
// module — optionally to a fixed list of resource addresses within it
// (`-target`, OpenTofu only) and to another revision of its repository. It
// is the per-run argument stored on the run row (args) of a
// component-scoped run; a run without one covers the whole workflow.
type RunScope struct {
	ComponentID uuid.UUID `json:"component_id"`
	// ComponentName is the authored name at the time the run began, kept so
	// the history reads without a lookup of a component that may since have
	// been renamed or deleted.
	ComponentName string `json:"component_name"`
	// ComponentType is the component's type when the run began. Empty on
	// runs from before Helm and Manifest components could run alone, which
	// were all OpenTofu.
	ComponentType string `json:"component_type,omitempty"`
	// Targets is the resource addresses the run is limited to, rendered as
	// -target flags on the plan; empty covers the whole component.
	Targets []string `json:"targets,omitempty"`
	// GitRef is the branch, tag, or commit the run cloned in place of the
	// component's own git_ref; empty when it ran the component's own.
	GitRef string `json:"git_ref,omitempty"`
}

// ScopedRunOptions are a component-scoped run's optional arguments: the
// resource addresses to limit it to (OpenTofu only) and the revision of the
// component's repository to run against instead of its own git_ref.
type ScopedRunOptions struct {
	Targets []string
	GitRef  string
}

// ErrInvalidScopedAction is returned by BeginComponentRun for an action other
// than deploy or uninstall — the only two a component-scoped run performs.
// A handler maps it to 400.
var ErrInvalidScopedAction = errors.New("workflows: a component-scoped run is a deploy or an uninstall")

// ErrScopedRunUnsupported is returned by BeginComponentRun for a run a
// component of its type can't have on its own: a Helm or Manifest component
// is only ever uninstalled alone, without targets — it is deployed with the
// workflow. A handler maps it to 400.
var ErrScopedRunUnsupported = errors.New("workflows: a Helm or Manifest component runs on its own only to be uninstalled, without targets")

// ErrInvalidGitRef is returned by BeginComponentRun for a git ref that
// cannot name a branch, tag, or commit, or one given for a component that
// is not cloned from git. A handler maps it to 400.
var ErrInvalidGitRef = errors.New("workflows: invalid git ref")

// ValidateGitRef reports whether ref can name a branch, tag, or commit — the
// rules of git check-ref-format, plus no leading "-" so it can never read as
// a flag. It is a guard for a clear 400; the clone quotes the ref either way.
func ValidateGitRef(ref string) error {
	bad := func(why string) error { return fmt.Errorf("%w %q: %s", ErrInvalidGitRef, ref, why) }
	switch {
	case ref == "":
		return bad("empty")
	case len(ref) > 255:
		return bad("longer than 255 characters")
	case strings.HasPrefix(ref, "-"):
		return bad(`starts with "-"`)
	case strings.HasPrefix(ref, "/"), strings.HasSuffix(ref, "/"), strings.Contains(ref, "//"):
		return bad("has an empty path segment")
	case strings.HasSuffix(ref, "."), strings.HasSuffix(ref, ".lock"):
		return bad(`ends with "." or ".lock"`)
	case strings.Contains(ref, ".."), strings.Contains(ref, "@{"), ref == "@":
		return bad(`contains ".." or "@{"`)
	}
	for _, seg := range strings.Split(ref, "/") {
		if strings.HasPrefix(seg, ".") {
			return bad(`has a path segment starting with "."`)
		}
	}
	for _, r := range ref {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`~^:?*[\`, r) {
			return bad(fmt.Sprintf("contains %q", r))
		}
	}
	return nil
}

// BeginComponentRun opens a run limited to one component. For an OpenTofu
// component that is a per-component **destroy** (action uninstall — `tofu
// plan -destroy` then the apply, gated) or a **targeted deploy** (action
// deploy, with the targets as `-target` flags on the plan; an empty list
// deploys the whole module). A Helm or Manifest component can only be
// **uninstalled** on its own (its release, or what was applied from its
// path), with no targets (ErrScopedRunUnsupported otherwise). A git ref
// (opts.GitRef) clones that branch, tag, or commit in place of the
// component's own git_ref — validated by ValidateGitRef, and only for a
// component cloned from git (ErrInvalidGitRef otherwise). Its
// `${{ components.* }}` references resolve against the referenced
// components' latest recorded outputs, since they aren't in the run (see
// outputsLookup). The component must belong to the org-scoped application
// (ent's NotFoundError otherwise). The targets are validated
// (tofu.ValidateTargets; failures wrap tofu.ErrInvalidTarget, a 400), and
// the application's in-flight gate applies exactly as for any run
// (ErrRunInFlight).
//
// The run is the component's usual step (an OpenTofu plan → apply pair)
// with no dependencies on anything else. An uninstall is **always** gated
// regardless of the component's own flag — for OpenTofu the apply parks
// with the destroy plan to review; for Helm and Manifest the step parks
// before it runs — while a deploy keeps the component's own gate and
// approval policy. The scope is stored on the run row (args) as RunScope
// JSON for the history, and the targets and ref are folded into the
// snapshot node's config (plan flags, git_ref) — the as-run config the
// planner already reads — so the worker needs nothing beyond the snapshot.
func (s *Service) BeginComponentRun(ctx context.Context, orgID, appID, componentID uuid.UUID, action string, opts ScopedRunOptions) (*ent.WorkflowRun, error) {
	if action != ActionDeploy && action != ActionUninstall {
		return nil, ErrInvalidScopedAction
	}
	targets := opts.Targets
	if err := tofu.ValidateTargets(targets); err != nil {
		return nil, err
	}
	if opts.GitRef != "" {
		if err := ValidateGitRef(opts.GitRef); err != nil {
			return nil, err
		}
	}
	if _, err := s.getApp(ctx, orgID, appID); err != nil {
		return nil, err
	}
	comp, err := s.ent.Component.Query().
		Where(component.OrganizationID(orgID), component.ApplicationID(appID), component.ID(componentID)).
		WithStage(func(q *ent.WorkflowStageQuery) { q.Where(workflowstage.OrganizationID(orgID)) }).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if string(comp.Type) != TypeTerraform && (action != ActionUninstall || len(targets) > 0) {
		return nil, ErrScopedRunUnsupported
	}
	if opts.GitRef != "" && !clonesFromGit(comp) {
		return nil, fmt.Errorf("%w: %s is not cloned from git", ErrInvalidGitRef, comp.Name)
	}
	if err := s.assertNoRunInFlight(ctx, orgID, appID); err != nil {
		return nil, err
	}
	scope := RunScope{ComponentID: comp.ID, ComponentName: comp.Name, ComponentType: string(comp.Type), Targets: targets, GitRef: opts.GitRef}
	args, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	return s.createRun(ctx, orgID, appID, action, string(args), scopedSnapshot(comp, action, opts))
}

// scopedSnapshot builds the snapshot of a component-scoped run from the live
// component: the authored node alone (its as-run config, target,
// credentials, and installation, with no dependencies — nothing else is in
// the run), with the targets appended to an OpenTofu node's plan flags, the
// ref (if any) in place of its git_ref, and the gate forced on for an
// uninstall, then expanded exactly as a
// whole-workflow run would expand it (for OpenTofu, the same plan + apply
// unit ids, so the apply's recorded state lands under the component like
// any deploy). A pure function (unit-testable).
func scopedSnapshot(c *ent.Component, action string, opts ScopedRunOptions) GraphSnapshot {
	n := componentNode(c, uuid.Nil)
	if n.Type == TypeTerraform {
		n.Config = withTargets(n.Config, opts.Targets)
	}
	if opts.GitRef != "" {
		n.Config = withGitRef(n.Config, opts.GitRef)
	}
	// Alone in its run, a failure is simply the run's failure.
	n.ContinueOnFailure = false
	n.RequiresApproval = c.RequiresApproval || action == ActionUninstall
	stageID, stages := snapshotStageOf(c)
	n.StageID = stageID
	return GraphSnapshot{Stages: stages, Nodes: expandExecutionNodes([]GraphNode{n}, action)}
}

// withTargets returns a copy of a terraform config map whose plan_flags
// (a JSON string array, validated at write time) carry the targets as
// trailing `-target=<address>` flags. An empty target list returns the
// config unchanged (still a copy); unparseable existing flags are replaced
// rather than failing — the planner's own decode would have refused them.
// Never mutates the input map.
func withTargets(cfg map[string]string, targets []string) map[string]string {
	out := make(map[string]string, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	if len(targets) == 0 {
		return out
	}
	var flags []string
	if raw := cfg[terraformConfigPlanFlags]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &flags)
	}
	flags = append(flags, tofu.TargetFlags(targets)...)
	encoded, _ := json.Marshal(flags)
	out[terraformConfigPlanFlags] = string(encoded)
	return out
}

// ScopeOf decodes the scope stored on a component-scoped run's args. It
// returns ok=false for a run of any other action and for a whole-workflow
// deploy/uninstall (no args); a scoped run whose args do not decode is an
// error.
func ScopeOf(run *ent.WorkflowRun) (scope RunScope, ok bool, err error) {
	if a := string(run.Action); a != ActionDeploy && a != ActionUninstall {
		return RunScope{}, false, nil
	}
	if run.Args == "" {
		return RunScope{}, false, nil
	}
	if err := json.Unmarshal([]byte(run.Args), &scope); err != nil {
		return RunScope{}, false, fmt.Errorf("workflows: run %s: decode run scope: %w", run.ID, err)
	}
	if scope.ComponentID == uuid.Nil {
		return RunScope{}, false, fmt.Errorf("workflows: run %s: run scope names no component", run.ID)
	}
	return scope, true, nil
}
