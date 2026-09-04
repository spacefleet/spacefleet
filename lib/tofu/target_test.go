package tofu

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateTargets covers the address grammar: resource, module-qualified,
// data, and indexed addresses pass; empty, bare-type, whitespace, shell
// characters, duplicates, and over-long lists are refused with the sentinel.
func TestValidateTargets(t *testing.T) {
	t.Parallel()
	ok := []string{
		"aws_instance.web",
		"aws_instance.web[0]",
		`aws_instance.web["blue"]`,
		"module.vpc.aws_subnet.private",
		`module.vpc["prod"].aws_subnet.private[2]`,
		"data.aws_ami.ubuntu",
		"module.k8s-cluster.this",
	}
	if err := ValidateTargets(ok); err != nil {
		t.Errorf("valid list: %v", err)
	}
	if err := ValidateTargets(nil); err != nil {
		t.Errorf("empty list: %v", err)
	}
	bad := map[string][]string{
		"empty":       {""},
		"bare type":   {"aws_instance"},
		"whitespace":  {"aws_instance.web extra"},
		"shell":       {"aws_instance.web;rm"},
		"flag":        {"-target=aws_instance.web"},
		"unquoted":    {"aws_instance.web[blue]"},
		"leading dot": {".aws_instance.web"},
		"duplicate":   {"aws_instance.web", "aws_instance.web"},
		"too long":    {"aws_instance." + strings.Repeat("a", maxTargetLen)},
		"too many":    make([]string, maxTargets+1),
	}
	for name, list := range bad {
		if err := ValidateTargets(list); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: err = %v, want ErrInvalidTarget", name, err)
		}
	}
}

func TestTargetFlags(t *testing.T) {
	t.Parallel()
	if got := TargetFlags(nil); got != nil {
		t.Errorf("empty: %v", got)
	}
	got := TargetFlags([]string{"aws_instance.web", `module.vpc["prod"].aws_subnet.private`})
	want := []string{"-target=aws_instance.web", `-target=module.vpc["prod"].aws_subnet.private`}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("flags = %q, want %q", got, want)
	}
}
