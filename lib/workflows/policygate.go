package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/planpolicy"
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
	in := policy.Input{
		Application: policy.Ref{ID: app.ID.String(), Name: app.Name},
		Component:   policy.Ref{ID: node.ComponentID.String(), Name: strings.TrimSuffix(node.Name, tofuPlanNameSuffix)},
		Run:         policy.RunInput{ID: a.WorkflowRunID.String(), Action: a.Action, StartedBy: startedBy},
		Plan:        policy.NewPlanInput(plan),
	}
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
