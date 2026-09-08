package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/tofu"
)

const denyDBDeletes = `package spacefleet

deny contains msg if {
	some r in input.plan.resources
	r.action == "delete"
	startswith(r.type, "aws_db")
	msg := sprintf("%s would be destroyed", [r.address])
}
`

func TestParseAddress(t *testing.T) {
	t.Parallel()
	cases := map[string][4]string{
		"aws_instance.web":                        {"", "managed", "aws_instance", "web"},
		"aws_instance.web[0]":                     {"", "managed", "aws_instance", "web"},
		`aws_instance.web["blue"]`:                {"", "managed", "aws_instance", "web"},
		"data.aws_ami.ubuntu":                     {"", "data", "aws_ami", "ubuntu"},
		"module.vpc.aws_subnet.private":           {"module.vpc", "managed", "aws_subnet", "private"},
		`module.vpc["a.b"].aws_subnet.private[1]`: {`module.vpc["a.b"]`, "managed", "aws_subnet", "private"},
		"module.a.module.b.data.x.y":              {"module.a.module.b", "data", "x", "y"},
		"module.only":                             {"module.only", "managed", "", ""},
	}
	for addr, want := range cases {
		m, mode, typ, name := ParseAddress(addr)
		if got := [4]string{m, mode, typ, name}; got != want {
			t.Errorf("%s = %v, want %v", addr, got, want)
		}
	}
}

func TestCompile(t *testing.T) {
	t.Parallel()
	if err := Compile(denyDBDeletes); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, src := range map[string]string{
		"wrong package": "package other\n\ndeny contains \"x\" if { true }\n",
		"syntax":        "package spacefleet\n\ndeny contains msg if {\n",
		"undefined":     "package spacefleet\n\ndeny contains msg if { msg := nope(1) }\n",
	} {
		if err := Compile(src); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: err = %v, want ErrInvalidPolicy", name, err)
		}
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	plan := tofu.Plan{Found: true, HasChanges: true, Add: 1, Destroy: 1, Resources: []tofu.ResourceChange{
		{Address: "aws_db_instance.main", Action: tofu.ActionDelete, Detail: "will be destroyed"},
		{Address: "module.web.aws_instance.app[0]", Action: tofu.ActionCreate},
	}}
	in := Input{
		Application: Ref{ID: "a", Name: "web"},
		Component:   Ref{ID: "c", Name: "infra"},
		Run:         RunInput{ID: "r", Action: "deploy", StartedBy: "kyle@example.com"},
		Plan:        NewPlanInput(plan),
	}
	if in.Plan.Resources[1].Module != "module.web" || in.Plan.Resources[1].Type != "aws_instance" {
		t.Errorf("plan input = %+v", in.Plan.Resources[1])
	}
	policies := []Policy{
		{ID: "1", Name: "no-db-deletes", Rego: denyDBDeletes, Enforcement: EnforcementBlock},
		{ID: "2", Name: "limit-adds", Enforcement: EnforcementWarn, Rego: "package spacefleet\n\ndeny contains msg if {\n\tinput.plan.add > 0\n\tmsg := sprintf(\"%d resources added by %s\", [input.plan.add, input.run.started_by])\n}\n"},
		{ID: "3", Name: "clean", Enforcement: EnforcementBlock, Rego: "package spacefleet\n\ndeny contains msg if {\n\tinput.plan.replace > 10\n\tmsg := \"too many replacements\"\n}\n"},
		{ID: "4", Name: "broken", Enforcement: EnforcementWarn, Rego: "package spacefleet\n\ndeny contains msg if {\n\tmsg := input.plan.resources[100].address\n}\n"},
		{ID: "5", Name: "bad", Enforcement: EnforcementBlock, Rego: "package other\n"},
	}
	v := Evaluate(context.Background(), policies, in)
	if !v.Blocked || !v.Warned || len(v.Results) != 5 {
		t.Fatalf("verdict = %+v", v)
	}
	if got := v.Results[0].Violations; len(got) != 1 || got[0] != "aws_db_instance.main would be destroyed" {
		t.Errorf("no-db-deletes = %v", got)
	}
	if got := v.Results[1].Violations; len(got) != 1 || got[0] != "1 resources added by kyle@example.com" {
		t.Errorf("limit-adds = %v", got)
	}
	if got := v.Results[2]; len(got.Violations) != 0 || got.Error != "" {
		t.Errorf("clean = %+v", got)
	}
	if got := v.Results[3]; len(got.Violations) != 0 || got.Error != "" {
		// An out-of-range index is simply undefined in Rego: no message, no error.
		t.Errorf("undefined lookup = %+v", got)
	}
	if got := v.Results[4]; got.Error == "" || !strings.Contains(got.Error, "compile") {
		t.Errorf("bad policy = %+v, want a compile error", got)
	}

	// No violations anywhere: neither blocked nor warned.
	v = Evaluate(context.Background(), policies[2:3], in)
	if v.Blocked || v.Warned {
		t.Errorf("clean verdict = %+v", v)
	}
	// A block policy without a deny rule at all is a pass.
	v = Evaluate(context.Background(), []Policy{{ID: "6", Name: "empty", Enforcement: EnforcementBlock, Rego: "package spacefleet\n\nallow := true\n"}}, in)
	if v.Blocked || len(v.Results[0].Violations) != 0 {
		t.Errorf("no deny rule = %+v", v)
	}
}
