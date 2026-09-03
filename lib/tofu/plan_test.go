package tofu

import (
	"strings"
	"testing"
)

// samplePlanLogs is a realistic captured log of a deploy plan step: clone
// chatter, init output, a plan with one of each action, output changes, then
// the planfile-handover tail.
const samplePlanLogs = `Cloning into '/src'...
SPACEFLEET_CHART_REVISION=0123abcd

Initializing the backend...

Successfully configured the backend "s3"! OpenTofu will automatically
use this backend unless the backend configuration changes.
Initializing provider plugins...
- Reusing previous version of hashicorp/aws from the dependency lock file

OpenTofu has been successfully initialized!

Note: Objects have changed outside of OpenTofu

OpenTofu detected the following changes made outside of OpenTofu since the
last "tofu apply" which may have affected this plan:

  # aws_s3_bucket.logs has been deleted
  - resource "aws_s3_bucket" "logs" {
      - bucket = "acme-logs" -> null
    }


─────────────────────────────────────────────────────────────────────────────

OpenTofu used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create
  ~ update in-place
  - destroy
-/+ destroy and then create replacement
 <= read (data resources)

OpenTofu will perform the following actions:

  # data.aws_ami.ubuntu will be read during apply
  # (config refers to values not yet known)
 <= data "aws_ami" "ubuntu" {
      + id = (known after apply)
    }

  # aws_instance.web will be created
  + resource "aws_instance" "web" {
      + ami           = (known after apply)
      + instance_type = "t3.micro"
      + user_data     = (sensitive value)
    }

  # module.vpc.aws_subnet.private[0] will be updated in-place
  ~ resource "aws_subnet" "private" {
        id   = "subnet-1"
      ~ tags = {
          ~ "env" = "dev" -> "prod"
        }
    }

  # aws_db_instance.main must be replaced
-/+ resource "aws_db_instance" "main" {
      ~ engine_version = "14" -> "15" # forces replacement
    }

  # aws_s3_bucket.logs will be destroyed
  # (because aws_s3_bucket.logs is not in configuration)
  - resource "aws_s3_bucket" "logs" {
      - bucket = "acme-logs" -> null
    }

  # aws_s3_bucket.old has moved to aws_s3_bucket.renamed
    resource "aws_s3_bucket" "renamed" {
        id = "old"
    }

Plan: 2 to add, 1 to change, 2 to destroy.

Changes to Outputs:
  + instance_id = (known after apply)
  ~ db_endpoint = "old" -> (known after apply)

─────────────────────────────────────────────────────────────────────────────

Saved the plan to: tfplan

To perform exactly these actions, run the following command to apply:
    tofu apply "tfplan"
apk add --no-cache kubectl
secret/tfplan-run-node configured
`

func TestParsePlanChanges(t *testing.T) {
	p := ParsePlan(samplePlanLogs)
	if !p.Found {
		t.Fatal("plan not found")
	}
	if !p.HasChanges {
		t.Error("HasChanges = false, want true")
	}
	if p.Add != 2 || p.Change != 1 || p.Destroy != 2 {
		t.Errorf("totals = %d/%d/%d, want 2/1/2", p.Add, p.Change, p.Destroy)
	}
	if p.Replace != 1 {
		t.Errorf("Replace = %d, want 1", p.Replace)
	}
	if !p.OutputsChanged {
		t.Error("OutputsChanged = false, want true")
	}

	want := []struct{ addr, action string }{
		{"data.aws_ami.ubuntu", ActionRead},
		{"aws_instance.web", ActionCreate},
		{"module.vpc.aws_subnet.private[0]", ActionUpdate},
		{"aws_db_instance.main", ActionReplace},
		{"aws_s3_bucket.logs", ActionDelete},
		{"aws_s3_bucket.old", ActionMove},
	}
	if len(p.Resources) != len(want) {
		t.Fatalf("got %d resources, want %d: %+v", len(p.Resources), len(want), p.Resources)
	}
	for i, w := range want {
		got := p.Resources[i]
		if got.Address != w.addr || got.Action != w.action {
			t.Errorf("resource %d = %s/%s, want %s/%s", i, got.Address, got.Action, w.addr, w.action)
		}
		if !strings.HasPrefix(strings.TrimSpace(got.Diff), "# "+w.addr) {
			t.Errorf("resource %d diff should start with its heading, got %q", i, got.Diff)
		}
	}

	// The drift section before the separator is not part of the plan: the
	// deleted-outside-tofu bucket must not be double counted, and the body must
	// start at the actions heading.
	if got := p.Resources[4].Detail; got != "will be destroyed" {
		t.Errorf("destroy detail = %q", got)
	}
	if !strings.HasPrefix(p.Body, "OpenTofu will perform the following actions:") {
		t.Errorf("body should start at the actions heading, got %q", p.Body[:60])
	}
	if strings.Contains(p.Body, "Saved the plan to") || strings.Contains(p.Body, "secret/") {
		t.Errorf("body should stop before the handover tail:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "~ db_endpoint") {
		t.Error("body should include the output changes")
	}
	// Each resource's diff is exactly its own block: the replace block carries
	// its forces-replacement line and nothing from its neighbours.
	if d := p.Resources[3].Diff; !strings.Contains(d, "forces replacement") || strings.Contains(d, "aws_s3_bucket") {
		t.Errorf("replace diff wrong:\n%s", d)
	}
	// A sensitive value stays masked in the diff, as tofu printed it.
	if !strings.Contains(p.Resources[1].Diff, "(sensitive value)") {
		t.Error("sensitive attribute should be masked in the diff")
	}
}

func TestParsePlanNoChanges(t *testing.T) {
	logs := `Initializing the backend...

No changes. Your infrastructure matches the configuration.

OpenTofu has compared your real infrastructure against your configuration and
found no differences, so no changes are needed.
Note: You didn't use the -out option to save this plan, so OpenTofu can't
guarantee to take exactly these actions if you run "tofu apply" now.
`
	p := ParsePlan(logs)
	if !p.Found {
		t.Fatal("plan not found")
	}
	if p.HasChanges || len(p.Resources) != 0 || p.Add+p.Change+p.Destroy != 0 {
		t.Errorf("no-changes plan reported changes: %+v", p)
	}
	if !strings.HasPrefix(p.Body, "No changes.") || strings.Contains(p.Body, "Note: You didn't") {
		t.Errorf("body = %q", p.Body)
	}
}

func TestParsePlanDestroy(t *testing.T) {
	logs := `OpenTofu will perform the following actions:

  # aws_instance.web will be destroyed
  - resource "aws_instance" "web" {
      - ami = "ami-1" -> null
    }

Plan: 0 to add, 0 to change, 1 to destroy.

Changes to Outputs:
  - instance_id = "i-1" -> null
`
	p := ParsePlan(logs)
	if !p.Found || !p.HasChanges || p.Destroy != 1 || len(p.Resources) != 1 {
		t.Fatalf("destroy plan parsed wrong: %+v", p)
	}
	if p.Resources[0].Action != ActionDelete {
		t.Errorf("action = %s", p.Resources[0].Action)
	}
}

func TestParsePlanOutputsOnly(t *testing.T) {
	// Only outputs change: tofu prints no "Plan:" totals line and no actions
	// heading, just the outputs section after "No changes." — still a change.
	logs := `No changes. Your infrastructure matches the configuration.

Changes to Outputs:
  + greeting = "hi"

You can apply this plan to save these new output values to the OpenTofu
state, without changing any real infrastructure.
`
	p := ParsePlan(logs)
	if !p.Found || !p.HasChanges || !p.OutputsChanged || len(p.Resources) != 0 {
		t.Fatalf("outputs-only plan parsed wrong: %+v", p)
	}
	if strings.Contains(p.Body, "You can apply this plan") {
		t.Errorf("body should stop before the apply hint: %q", p.Body)
	}
}

func TestParsePlanAbsent(t *testing.T) {
	for _, logs := range []string{
		"",
		"Cloning into '/src'...\nfatal: could not read Username\n",
		"Error: Backend initialization required\n",
	} {
		if p := ParsePlan(logs); p.Found || p.HasChanges || len(p.Resources) != 0 || p.Body != "" {
			t.Errorf("%q: expected an absent plan, got %+v", logs, p)
		}
	}
}

func TestClassifyHeading(t *testing.T) {
	cases := map[string]string{
		"will be created":                 ActionCreate,
		"will be updated in-place":        ActionUpdate,
		"must be replaced":                ActionReplace,
		"is tainted, so must be replaced": ActionReplace,
		"will be replaced, as requested":  ActionReplace,
		"will be destroyed":               ActionDelete,
		"will be read during apply":       ActionRead,
		"has moved to aws_x.y":            ActionMove,
		"will be imported":                ActionImport,
		"will no longer be managed by OpenTofu, but will not be destroyed": ActionForget,
		"does something new": ActionOther,
	}
	for phrase, want := range cases {
		if got, detail := classifyHeading(phrase); got != want || detail != phrase {
			t.Errorf("classifyHeading(%q) = %s/%q, want %s", phrase, got, detail, want)
		}
	}
}

// TestParsePlanDriftSection proves the "changes made outside of OpenTofu"
// section a normal plan prints before its actions is read into Drift — kept
// apart from the planned actions, which are unaffected.
func TestParsePlanDriftSection(t *testing.T) {
	p := ParsePlan(samplePlanLogs)
	if !p.HasDrift || len(p.Drift) != 1 {
		t.Fatalf("drift = %+v", p.Drift)
	}
	if d := p.Drift[0]; d.Address != "aws_s3_bucket.logs" || d.Action != ActionDriftDelete || !strings.Contains(d.Diff, `- bucket = "acme-logs" -> null`) {
		t.Errorf("drift[0] = %+v", d)
	}
	if p.RefreshOnly {
		t.Error("a normal plan is not refresh-only")
	}
	if len(p.Resources) != 6 {
		t.Errorf("planned actions must be unaffected by the drift section, got %d", len(p.Resources))
	}
}

// TestParsePlanRefreshOnlyDrift proves a drift check (refresh-only plan) that
// found drift — which prints no actions heading and no "No changes." verdict,
// only the drift section and the refresh-only explanation — is Found, has
// changes (the drift), and carries the drifted resources.
func TestParsePlanRefreshOnlyDrift(t *testing.T) {
	logs := `Initializing the backend...

Note: Objects have changed outside of OpenTofu

OpenTofu detected the following changes made outside of OpenTofu since the
last "tofu apply" which may have affected this plan:

  # aws_instance.web has been changed
  ~ resource "aws_instance" "web" {
        id            = "i-1"
      ~ instance_type = "t3.micro" -> "t3.small"
        # (5 unchanged attributes hidden)
    }

  # aws_s3_bucket.logs has been deleted
  - resource "aws_s3_bucket" "logs" {
      - bucket = "acme-logs" -> null
    }


This is a refresh-only plan, so OpenTofu will not take any actions to undo
these. If you were expecting these changes then you can apply this plan to
record the updated values in the OpenTofu state without changing any real
infrastructure.
`
	p := ParsePlan(logs)
	if !p.Found || !p.RefreshOnly || !p.HasDrift || !p.HasChanges {
		t.Fatalf("refresh-only drift parsed wrong: %+v", p)
	}
	if len(p.Drift) != 2 || p.Drift[0].Action != ActionDriftUpdate || p.Drift[1].Action != ActionDriftDelete {
		t.Errorf("drift = %+v", p.Drift)
	}
	if !strings.Contains(p.Drift[0].Diff, `"t3.micro" -> "t3.small"`) {
		t.Errorf("drift diff should carry the state-vs-real change: %q", p.Drift[0].Diff)
	}
	if len(p.Resources) != 0 || p.Add+p.Change+p.Destroy != 0 {
		t.Errorf("a refresh-only plan proposes no actions: %+v", p)
	}
	if strings.Contains(p.Body, "This is a refresh-only plan") || !strings.Contains(p.Body, "aws_instance.web has been changed") {
		t.Errorf("body = %q", p.Body)
	}
}

// TestParsePlanRefreshOnlyClean proves a drift check with nothing drifted reads
// as found, refresh-only, no drift, no changes.
func TestParsePlanRefreshOnlyClean(t *testing.T) {
	logs := `No changes. Your infrastructure still matches the configuration.

OpenTofu has checked that the real remote objects still match the result of
your most recent changes, and found no differences.
`
	p := ParsePlan(logs)
	if !p.Found || !p.RefreshOnly || p.HasDrift || p.HasChanges || len(p.Drift) != 0 {
		t.Fatalf("clean refresh-only parsed wrong: %+v", p)
	}
}
