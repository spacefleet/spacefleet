package tofu

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestStateOpValidate proves the fixed menu: each operation needs exactly its
// fields, rejects the others, and every field is a single whitespace-free
// token.
func TestStateOpValidate(t *testing.T) {
	t.Parallel()
	valid := []StateOp{
		{Operation: StateOpForceUnlock, LockID: "3b2f9c0e-1d4a-4f8b-9c2d-7e6a5b4c3d2e"},
		{Operation: StateOpRemove, Address: "aws_instance.web"},
		{Operation: StateOpMove, Address: "aws_instance.web", NewAddress: "module.web.aws_instance.this[0]"},
		{Operation: StateOpImport, Address: "aws_s3_bucket.data", ImportID: "acme-data"},
	}
	for _, op := range valid {
		if err := op.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", op, err)
		}
	}
	invalid := []struct {
		op   StateOp
		want string
	}{
		{StateOp{}, "operation is required"},
		{StateOp{Operation: "state pull"}, "unknown operation"},
		{StateOp{Operation: StateOpForceUnlock}, "requires lock_id"},
		{StateOp{Operation: StateOpForceUnlock, LockID: "x", Address: "a"}, "address does not apply"},
		{StateOp{Operation: StateOpRemove}, "requires address"},
		{StateOp{Operation: StateOpRemove, Address: "aws_instance.web extra"}, "must not contain whitespace"},
		{StateOp{Operation: StateOpRemove, Address: "a\nb"}, "must not contain whitespace"},
		{StateOp{Operation: StateOpRemove, Address: strings.Repeat("a", maxStateOpFieldLen+1)}, "too long"},
		{StateOp{Operation: StateOpMove, Address: "a"}, "requires new_address"},
		{StateOp{Operation: StateOpMove, Address: "a", NewAddress: "a"}, "must differ"},
		{StateOp{Operation: StateOpImport, Address: "a"}, "requires import_id"},
		{StateOp{Operation: StateOpImport, Address: "a", ImportID: "i", LockID: "l"}, "lock_id does not apply"},
	}
	for _, tc := range invalid {
		err := tc.op.Validate()
		if err == nil || !errors.Is(err, ErrInvalidStateOp) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want ErrInvalidStateOp containing %q", tc.op, err, tc.want)
		}
	}
}

// TestStateOpCommand proves the display command is the operation's real argv
// (quoted only where needed) and that an import carries just the -var /
// -var-file plan flags.
func TestStateOpCommand(t *testing.T) {
	t.Parallel()
	planFlags := []string{"-var=env=prod", "-target=aws_instance.web", "-var-file", "prod.tfvars", "-refresh=false", "-var-file=x.tfvars"}
	cases := []struct {
		op   StateOp
		want string
	}{
		{StateOp{Operation: StateOpForceUnlock, LockID: "abc-123"}, "tofu force-unlock -force abc-123"},
		{StateOp{Operation: StateOpRemove, Address: `aws_instance.web["a b"]`}, `tofu state rm 'aws_instance.web["a b"]'`},
		{StateOp{Operation: StateOpMove, Address: "aws_instance.a", NewAddress: "module.m.aws_instance.a[0]"}, "tofu state mv aws_instance.a module.m.aws_instance.a[0]"},
		{StateOp{Operation: StateOpImport, Address: "aws_s3_bucket.data", ImportID: "acme-data"},
			"tofu import -input=false -no-color -var=env=prod -var-file prod.tfvars -var-file=x.tfvars aws_s3_bucket.data acme-data"},
		{StateOp{Operation: StateOpRemove, Address: "a'b"}, `tofu state rm 'a'\''b'`},
	}
	for _, tc := range cases {
		if got := tc.op.Command(planFlags); got != tc.want {
			t.Errorf("%+v.Command() = %q, want %q", tc.op, got, tc.want)
		}
	}
	if got := ImportFlags([]string{"-target=x", "-var"}); got != nil {
		t.Errorf("a trailing -var with no value must be dropped, got %v", got)
	}
	if got, want := ImportFlags(planFlags), []string{"-var=env=prod", "-var-file", "prod.tfvars", "-var-file=x.tfvars"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ImportFlags = %v, want %v", got, want)
	}
	for _, op := range []StateOp{{Operation: StateOpForceUnlock, LockID: "x"}, {Operation: StateOpRemove, Address: "a"}, {Operation: StateOpMove, Address: "a", NewAddress: "b"}, {Operation: StateOpImport, Address: "a", ImportID: "i"}} {
		if op.Label() == op.Operation && op.Operation != StateOpImport {
			t.Errorf("%s has no label", op.Operation)
		}
	}
}
