package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/planpolicy"
	"github.com/spacefleet/spacefleet/ent/predicate"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/policy"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// The policy gate: after an OpenTofu plan unit succeeds, every enabled
// policy of the organization that covers the application is evaluated
// against the parsed plan (lib/policy). The verdict is recorded on the plan
// step for the run view and the approval gate; when a block policy is
// violated (or cannot be evaluated — fail closed) on a deploy or uninstall,
// the plan step fails, so its apply is skipped and the run fails. A preview
// records the verdict without failing — it is the way to see what a policy
// would do.

// policyGateApplies reports whether a unit's success passes through the
// policy gate: an OpenTofu plan unit of a deploy, uninstall, or preview run
// (a drift check has nothing to apply; a state operation plans nothing).
func policyGateApplies(action string, node GraphNode) bool {
	if node.Type != TypeTerraform || node.Config[terraformConfigCommand] != terraformCommandPlan {
		return false
	}
	return action == ActionDeploy || action == ActionUninstall || action == ActionPreview
}

// policiesFor loads the enabled policies that cover an application: the
// organization's policies with no application limit, plus those limited to
// this application. Org-scoped.
func (s *Service) policiesFor(ctx context.Context, orgID, appID uuid.UUID) ([]policy.Policy, error) {
	rows, err := s.ent.PlanPolicy.Query().
		Where(
			planpolicy.OrganizationID(orgID),
			planpolicy.Enabled(true),
			planpolicy.Or(planpolicy.ApplicationIDIsNil(), planpolicy.ApplicationID(appID)),
		).
		Order(ent.Asc(planpolicy.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]policy.Policy, 0, len(rows))
	for _, r := range rows {
		out = append(out, policy.Policy{ID: r.ID.String(), Name: r.Name, Rego: r.Rego, Enforcement: string(r.Enforcement)})
	}
	return out, nil
}

// evaluatePolicies runs the gate for a succeeded plan unit: loads the
// applicable policies, parses the plan from the step's logs, evaluates,
// records the verdict on the step, and reports whether the step must fail
// (blocked, and the action applies). No policies means no verdict and no
// gate. A load failure fails closed only when there are policies to
// consider: it is reported as a blocking verdict rather than silently
// passing the plan.
func (w *WorkflowRunWorker) evaluatePolicies(ctx context.Context, a WorkflowRunArgs, app *ent.Application, node GraphNode, crID uuid.UUID, logs string) (blocked bool, reason string) {
	policies, err := w.svc.policiesFor(ctx, a.OrgID, app.ID)
	if err != nil {
		log.Printf("worker: workflow run %s: load policies: %v", a.WorkflowRunID, err)
		return a.Action != ActionPreview, "policies could not be loaded: " + err.Error()
	}
	if len(policies) == 0 {
		return false, ""
	}
	plan := tofu.ParsePlan(logs)
	run, err := w.svc.ent.WorkflowRun.Get(ctx, a.WorkflowRunID)
	startedBy := ""
	if err == nil {
		startedBy = run.StartedBy
	}
	in := policyInput(app, node.ComponentID, node.Name, policy.RunInput{ID: a.WorkflowRunID.String(), Action: a.Action, StartedBy: startedBy}, plan)
	verdict := policy.Evaluate(ctx, policies, in)
	if raw, err := json.Marshal(verdict); err == nil {
		if err := w.svc.SetComponentRunPolicy(ctx, a.OrgID, crID, string(raw)); err != nil {
			log.Printf("worker: workflow run %s: record policy verdict: %v", a.WorkflowRunID, err)
		}
	}
	if !verdict.Blocked || a.Action == ActionPreview {
		return false, ""
	}
	return true, "blocked by policy: " + verdictSummary(verdict)
}

// policyInput assembles the document a policy sees for one plan step — the
// single place the contract is built, shared by the gate and the dry run
// (PolicyInputFor). The component name is the authored one (the plan unit's
// display suffix trimmed).
func policyInput(app *ent.Application, componentID uuid.UUID, componentName string, run policy.RunInput, plan tofu.Plan) policy.Input {
	return policy.Input{
		Application: policy.Ref{ID: app.ID.String(), Name: app.Name},
		Component:   policy.Ref{ID: componentID.String(), Name: strings.TrimSuffix(componentName, tofuPlanNameSuffix)},
		Run:         run,
		Plan:        policy.NewPlanInput(plan),
	}
}

// PlanStep is a settled OpenTofu plan step with the run and application it
// belongs to — what a policy dry run evaluates against.
type PlanStep struct {
	Step *ent.ComponentRun
	Run  *ent.WorkflowRun
	App  *ent.Application
}

// PolicyInputFor builds the policy input for a plan step exactly as the
// gate did when the step settled (same parser, same contract), so a dry
// run against a past plan answers "what would this policy have said".
func PolicyInputFor(ps PlanStep) policy.Input {
	run := policy.RunInput{ID: ps.Run.ID.String(), Action: string(ps.Run.Action), StartedBy: ps.Run.StartedBy}
	return policyInput(ps.App, ps.Step.ComponentID, ps.Step.Name, run, tofu.ParsePlan(ps.Step.Logs))
}

// planStepActions are the run actions whose OpenTofu plan units pass the
// policy gate (policyGateApplies) — the steps a dry run may target.
var planStepActions = []workflowrun.Action{workflowrun.ActionDeploy, workflowrun.ActionUninstall, workflowrun.ActionPreview}

// ComponentName is the authored component's name — the plan unit's display
// suffix trimmed — the same name the policy input carries.
func (ps PlanStep) ComponentName() string {
	return strings.TrimSuffix(ps.Step.Name, tofuPlanNameSuffix)
}

// planStepPredicates select the organization's succeeded OpenTofu plan
// units: the run action must be one the gate covers, and the step must be
// the plan unit expandExecutionNodes produced (its name carries the plan
// suffix; the apply unit's carries the apply suffix; a state-op unit is
// excluded by its run action). Recognising the unit by its own snapshot
// name — not by whether the authored component still exists — keeps plans
// of since-removed components available.
func planStepPredicates(orgID uuid.UUID) []predicate.ComponentRun {
	return []predicate.ComponentRun{
		componentrun.OrganizationID(orgID),
		componentrun.TypeEQ(TypeTerraform),
		componentrun.NameHasSuffix(tofuPlanNameSuffix),
		componentrun.StatusEQ(componentrun.StatusSucceeded),
		componentrun.HasWorkflowRunWith(workflowrun.OrganizationID(orgID), workflowrun.ActionIn(planStepActions...)),
	}
}

// RecentPlanSteps returns the organization's most recent succeeded OpenTofu
// plan steps (newest first, at most limit), each with its run and
// application, for picking a plan to dry-run a policy against. Org-scoped.
func (s *Service) RecentPlanSteps(ctx context.Context, orgID uuid.UUID, limit int) ([]PlanStep, error) {
	rows, err := s.ent.ComponentRun.Query().
		Where(append(planStepPredicates(orgID), componentrun.LogsNEQ(""))...).
		WithWorkflowRun(func(q *ent.WorkflowRunQuery) { q.WithApplication() }).
		Order(ent.Desc(componentrun.FieldFinishedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return planSteps(rows), nil
}

// PlanStep returns one succeeded OpenTofu plan step of the organization
// with its run and application, or ent's NotFoundError when the id names
// anything else (another org's step, an apply unit, a failed plan).
func (s *Service) PlanStep(ctx context.Context, orgID, componentRunID uuid.UUID) (PlanStep, error) {
	cr, err := s.ent.ComponentRun.Query().
		Where(append(planStepPredicates(orgID), componentrun.ID(componentRunID))...).
		WithWorkflowRun(func(q *ent.WorkflowRunQuery) { q.WithApplication() }).
		Only(ctx)
	if err != nil {
		return PlanStep{}, err
	}
	steps := planSteps([]*ent.ComponentRun{cr})
	if len(steps) == 0 {
		return PlanStep{}, &ent.NotFoundError{}
	}
	return steps[0], nil
}

// planSteps pairs each row with its loaded run and application, dropping a
// row whose run or application is gone (an application delete cascades, so
// this is defensive), preserving order.
func planSteps(rows []*ent.ComponentRun) []PlanStep {
	out := make([]PlanStep, 0, len(rows))
	for _, cr := range rows {
		run := cr.Edges.WorkflowRun
		if run == nil || run.Edges.Application == nil {
			continue
		}
		out = append(out, PlanStep{Step: cr, Run: run, App: run.Edges.Application})
	}
	return out
}

// verdictSummary renders the blocking policies' first messages for a step's
// failure message.
func verdictSummary(v policy.Verdict) string {
	var parts []string
	for _, r := range v.Results {
		if r.Enforcement != policy.EnforcementBlock {
			continue
		}
		switch {
		case r.Error != "":
			parts = append(parts, fmt.Sprintf("%s could not be evaluated (%s)", r.PolicyName, r.Error))
		case len(r.Violations) > 0:
			parts = append(parts, fmt.Sprintf("%s: %s", r.PolicyName, strings.Join(r.Violations, "; ")))
		}
	}
	return strings.Join(parts, " · ")
}

// PolicyVerdictOf decodes the verdict stored on a plan step; ok is false
// when none was recorded.
func PolicyVerdictOf(cr *ent.ComponentRun) (v policy.Verdict, ok bool) {
	if cr.Policy == "" {
		return policy.Verdict{}, false
	}
	if err := json.Unmarshal([]byte(cr.Policy), &v); err != nil {
		return policy.Verdict{}, false
	}
	return v, true
}
