package workflows

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// TestNormalizeApprovalPolicy: approvers are trimmed, lowercased, and
// deduplicated; the counts are bounded; a bad policy is an ErrInvalidConfig.
func TestNormalizeApprovalPolicy(t *testing.T) {
	t.Parallel()
	node := func(p ApprovalPolicy) ComponentInput {
		return ComponentInput{ID: uuid.New(), Name: "web", Type: TypeHelm, ApprovalPolicy: p}
	}
	got, err := normalizeApprovalPolicy(node(ApprovalPolicy{
		Approvers: []string{" Ops@Example.com ", "ops@example.com", "sre@example.com"},
		Required:  2, RequireDifferentApprover: true, TimeoutMinutes: 60,
	}))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := ApprovalPolicy{Approvers: []string{"ops@example.com", "sre@example.com"}, Required: 2, RequireDifferentApprover: true, TimeoutMinutes: 60}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalized = %+v, want %+v", got, want)
	}
	zero, err := normalizeApprovalPolicy(node(ApprovalPolicy{}))
	if err != nil || !isZeroPolicy(zero) || zero.Approvers != nil {
		t.Errorf("zero policy = %+v (err %v)", zero, err)
	}
	bad := []ApprovalPolicy{
		{Approvers: []string{""}},
		{Approvers: []string{"a b@example.com"}},
		{Approvers: []string{"a@example.com"}, Required: 2},
		{Required: -1},
		{TimeoutMinutes: -5},
		{TimeoutMinutes: maxApprovalTimeoutMinutes + 1},
	}
	for _, p := range bad {
		if _, err := normalizeApprovalPolicy(node(p)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%+v: err = %v, want ErrInvalidConfig", p, err)
		}
	}
	// Required without named approvers is fine (any N editors).
	if _, err := normalizeApprovalPolicy(node(ApprovalPolicy{Required: 3})); err != nil {
		t.Errorf("required without approvers: %v", err)
	}
}

// TestSnapshotPolicyAndApprovals: the policy is read off the snapshot node by
// unit id, the default policy reads as nil, and the approvals tally decodes.
func TestSnapshotPolicyAndApprovals(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	graph := `{"nodes":[{"id":"` + id.String() + `","component_id":"` + id.String() + `","name":"x","type":"helm","config":{},"depends_on":[],"approval_policy":{"approvers":["a@example.com"],"required":1,"timeout_minutes":30}}]}`
	p := snapshotPolicy(graph, id)
	if p == nil || p.Approvers[0] != "a@example.com" || p.TimeoutMinutes != 30 {
		t.Errorf("policy = %+v", p)
	}
	if snapshotPolicy(graph, uuid.New()) != nil || snapshotPolicy("", id) != nil || snapshotPolicy("{", id) != nil {
		t.Error("unknown unit / empty / garbled snapshot must read as the default policy")
	}
	if approvalsRequired(nil) != 1 || approvalsRequired(&ApprovalPolicy{Required: 0}) != 1 || approvalsRequired(&ApprovalPolicy{Required: 3}) != 3 {
		t.Error("approvalsRequired wrong")
	}
	if got := ParseApprovals(`[{"by":"a@example.com","at":"2026-09-04T10:00:00Z"}]`); len(got) != 1 || got[0].By != "a@example.com" {
		t.Errorf("ParseApprovals = %+v", got)
	}
	if ParseApprovals("") != nil || ParseApprovals("nope") != nil {
		t.Error("empty/garbled approvals must be nil")
	}
}
