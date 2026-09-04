package api

import (
	"testing"

	"github.com/spacefleet/spacefleet/ent"
)

// tofuPlanLogs is a settled OpenTofu plan step's captured output: one create,
// one replacement, and an output change.
const tofuPlanLogs = `Initializing the backend...

OpenTofu will perform the following actions:

  # aws_instance.web will be created
  + resource "aws_instance" "web" {
      + instance_type = "t3.micro"
      + user_data     = (sensitive value)
    }

  # aws_db_instance.main must be replaced
-/+ resource "aws_db_instance" "main" {
      ~ engine_version = "14" -> "15" # forces replacement
    }

Plan: 2 to add, 0 to change, 1 to destroy.

Changes to Outputs:
  + instance_id = (known after apply)

Saved the plan to: tfplan
`

// TestToAPIComponentRun_TofuPlan proves an OpenTofu plan step's captured logs
// surface as a structured plan on both the list and detail shapes, with the
// per-resource diff blocks (and the plan body) gated to editor-or-above while
// the counts, addresses, and has_changes verdict are visible to everyone.
func TestToAPIComponentRun_TofuPlan(t *testing.T) {
	cr := &ent.ComponentRun{Type: "terraform", Name: "infra · plan", Logs: tofuPlanLogs}

	list := toAPIComponentRun(cr, false)
	if list.Plan == nil {
		t.Fatal("list shape: plan missing")
	}
	if !list.Plan.HasChanges || list.Plan.Add != 2 || list.Plan.Destroy != 1 || list.Plan.Replace != 1 {
		t.Errorf("list plan summary wrong: %+v", *list.Plan)
	}
	if len(list.Plan.Resources) != 2 || list.Plan.Resources[1].Action != PlanResourceChangeActionReplace {
		t.Errorf("list plan resources wrong: %+v", list.Plan.Resources)
	}
	for _, r := range list.Plan.Resources {
		if r.Diff != nil {
			t.Errorf("list shape must not carry resource diffs: %+v", r)
		}
	}

	viewer := toAPIComponentRunDetail(cr, false)
	if viewer.Diff != nil || viewer.Logs != nil {
		t.Error("viewer must not see the plan body or logs")
	}
	if viewer.HasChanges == nil || !*viewer.HasChanges {
		t.Error("viewer should still see has_changes")
	}
	if viewer.Plan == nil || len(viewer.Plan.Resources) != 2 || viewer.Plan.Resources[0].Diff != nil {
		t.Errorf("viewer should see addresses/actions but no diffs: %+v", viewer.Plan)
	}

	editor := toAPIComponentRunDetail(cr, true)
	if editor.Diff == nil || editor.Plan == nil {
		t.Fatal("editor should see the plan body and structure")
	}
	if d := editor.Plan.Resources[1].Diff; d == nil || *d == "" {
		t.Error("editor should see per-resource diffs")
	}
	if editor.Plan.OutputsChanged == nil || !*editor.Plan.OutputsChanged {
		t.Error("outputs_changed should be set")
	}
}

// TestToAPIComponentRun_NoPlan proves a non-OpenTofu step, and an OpenTofu step
// whose logs hold no plan (e.g. the apply unit), carry no plan field.
func TestToAPIComponentRun_NoPlan(t *testing.T) {
	for _, cr := range []*ent.ComponentRun{
		{Type: "helm", Logs: tofuPlanLogs},
		{Type: "terraform", Logs: "aws_instance.web: Creating...\nApply complete! Resources: 1 added, 0 changed, 0 destroyed.\n"},
		{Type: "terraform"},
	} {
		if got := toAPIComponentRun(cr, true); got.Plan != nil {
			t.Errorf("%+v: unexpected plan %+v", cr, got.Plan)
		}
		if got := toAPIComponentRunDetail(cr, true); got.Plan != nil {
			t.Errorf("%+v: unexpected plan on detail", cr)
		}
	}
}
