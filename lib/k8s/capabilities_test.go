package k8s

import (
	"context"
	"errors"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// allowFunc decides whether a given (group, resource, subresource, verb) is
// allowed for a test.
type allowFunc func(attrs authzv1.ResourceAttributes) bool

// fakeClientset returns a fake clientset wired with reactors that (1) report the
// given identity from a SelfSubjectReview and (2) answer each
// SelfSubjectAccessReview via allow.
func fakeClientset(identity authnv1.UserInfo, allow allowFunc) *fake.Clientset {
	cs := fake.NewSimpleClientset()

	cs.PrependReactor("create", "selfsubjectreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssr := &authnv1.SelfSubjectReview{}
		ssr.Status.UserInfo = identity
		return true, ssr, nil
	})

	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		sar := create.GetObject().(*authzv1.SelfSubjectAccessReview)
		out := sar.DeepCopy()
		out.Status.Allowed = sar.Spec.ResourceAttributes != nil && allow(*sar.Spec.ResourceAttributes)
		if !out.Status.Allowed {
			out.Status.Reason = "not allowed in test"
		}
		return true, out, nil
	})

	return cs
}

func findCap(report *CapabilityReport, key string) (CapabilityResult, bool) {
	for _, c := range report.Capabilities {
		if c.Key == key {
			return c, true
		}
	}
	return CapabilityResult{}, false
}

func TestInspectParsesIdentity(t *testing.T) {
	identity := authnv1.UserInfo{
		Username: "system:serviceaccount:spacefleet:reader",
		UID:      "uid-123",
		Groups:   []string{"system:serviceaccounts", "viewers"},
	}
	cs := fakeClientset(identity, func(authzv1.ResourceAttributes) bool { return true })

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Identity.Username != identity.Username {
		t.Errorf("Username = %q, want %q", report.Identity.Username, identity.Username)
	}
	if report.Identity.UID != identity.UID {
		t.Errorf("UID = %q, want %q", report.Identity.UID, identity.UID)
	}
	if len(report.Identity.Groups) != 2 {
		t.Errorf("Groups = %v, want 2", report.Identity.Groups)
	}
}

// TestInspectAllAllowed confirms that when every access review is allowed, every
// checked capability (read and write) reports allowed with no failures.
func TestInspectAllAllowed(t *testing.T) {
	cs := fakeClientset(authnv1.UserInfo{Username: "admin"}, func(authzv1.ResourceAttributes) bool { return true })

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	want := []string{
		"view_nodes", "view_namespaces", "view_pods", "view_pod_logs",
		"restart_workloads", "scale_workloads", "manage_helm_releases",
	}
	if len(report.Capabilities) != len(want) {
		t.Fatalf("got %d capabilities, want %d: %+v", len(report.Capabilities), len(want), report.Capabilities)
	}
	for _, key := range want {
		c, ok := findCap(report, key)
		if !ok {
			t.Errorf("missing capability %q", key)
			continue
		}
		if !c.Allowed {
			t.Errorf("capability %q should be allowed, failed=%+v", key, c.Failed)
		}
		if len(c.Failed) != 0 {
			t.Errorf("capability %q allowed but has Failed=%+v", key, c.Failed)
		}
	}
}

// TestInspectSkipsFutureCapabilities confirms the Future flag is honored: a
// capability marked Future is declared but never checked. Driven with a
// synthetic catalog so the assertion is independent of the real catalog, which
// has no future rows today.
func TestInspectSkipsFutureCapabilities(t *testing.T) {
	caps := []Capability{
		{Key: "checked", Name: "Checked", Rules: []Rule{
			{APIGroup: "", Resource: "pods", Verbs: []string{"list"}, ClusterScoped: true},
		}},
		{Key: "later", Name: "Later", Future: true, Rules: []Rule{
			{APIGroup: "*", Resource: "*", Verbs: []string{"*"}, ClusterScoped: true},
		}},
	}
	cs := fakeClientset(authnv1.UserInfo{Username: "admin"}, func(authzv1.ResourceAttributes) bool { return true })

	report, err := inspect(context.Background(), cs, caps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if _, ok := findCap(report, "checked"); !ok {
		t.Error("checked capability should be present")
	}
	if _, ok := findCap(report, "later"); ok {
		t.Error("future capability should not be checked")
	}
}

// TestInspectWriteCapabilityDenied confirms a denied write verb flips only the
// write capability that needs it, leaving reads (and other writes) intact.
func TestInspectWriteCapabilityDenied(t *testing.T) {
	// Deny patch on apps/deployments. restart_workloads (needs deployments patch)
	// must fail; scale_workloads uses the scale subresource and stays allowed.
	allow := func(a authzv1.ResourceAttributes) bool {
		if a.Group == "apps" && a.Resource == "deployments" && a.Subresource == "" && a.Verb == "patch" {
			return false
		}
		return true
	}
	cs := fakeClientset(authnv1.UserInfo{Username: "operator"}, allow)

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	restart, ok := findCap(report, "restart_workloads")
	if !ok {
		t.Fatal("missing restart_workloads")
	}
	if restart.Allowed {
		t.Error("restart_workloads should be denied when deployments/patch is denied")
	}
	if len(restart.Failed) != 1 || restart.Failed[0].Rule.Resource != "deployments" || restart.Failed[0].Verb != "patch" {
		t.Errorf("restart_workloads Failed = %+v, want exactly deployments/patch", restart.Failed)
	}

	if scale, _ := findCap(report, "scale_workloads"); !scale.Allowed {
		t.Errorf("scale_workloads should stay allowed (uses the scale subresource), failed=%+v", scale.Failed)
	}
	if nodes, _ := findCap(report, "view_nodes"); !nodes.Allowed {
		t.Errorf("reads should stay allowed, view_nodes failed=%+v", nodes.Failed)
	}
}

// TestInspectMapsVerbsToCapabilities checks that denying a single verb of a
// single resource flips only the capability that depends on it, and records the
// exact denied rule/verb in Failed.
func TestInspectMapsVerbsToCapabilities(t *testing.T) {
	// Deny only nodes/watch. view_nodes (needs list+watch) must fail; the other
	// read capabilities stay allowed.
	allow := func(a authzv1.ResourceAttributes) bool {
		if a.Resource == "nodes" && a.Verb == "watch" {
			return false
		}
		return true
	}
	cs := fakeClientset(authnv1.UserInfo{Username: "partial"}, allow)

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	nodes, ok := findCap(report, "view_nodes")
	if !ok {
		t.Fatal("missing view_nodes")
	}
	if nodes.Allowed {
		t.Error("view_nodes should be denied when nodes/watch is denied")
	}
	if len(nodes.Failed) != 1 {
		t.Fatalf("view_nodes Failed = %+v, want exactly 1", nodes.Failed)
	}
	if got := nodes.Failed[0]; got.Rule.Resource != "nodes" || got.Verb != "watch" {
		t.Errorf("Failed[0] = %+v, want nodes/watch", got)
	}
	if nodes.Failed[0].Reason == "" {
		t.Error("expected a denial reason on the failed rule")
	}

	for _, key := range []string{"view_namespaces", "view_pods", "view_pod_logs"} {
		c, _ := findCap(report, key)
		if !c.Allowed {
			t.Errorf("capability %q should remain allowed, failed=%+v", key, c.Failed)
		}
	}
}

// TestInspectSubresourceMapping confirms the pods/log subresource is checked
// distinctly from plain pods: denying pods/log only flips view_pod_logs.
func TestInspectSubresourceMapping(t *testing.T) {
	allow := func(a authzv1.ResourceAttributes) bool {
		return a.Resource != "pods" || a.Subresource != "log"
	}
	cs := fakeClientset(authnv1.UserInfo{Username: "no-logs"}, allow)

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	logs, _ := findCap(report, "view_pod_logs")
	if logs.Allowed {
		t.Error("view_pod_logs should be denied when pods/log get is denied")
	}
	pods, _ := findCap(report, "view_pods")
	if !pods.Allowed {
		t.Errorf("view_pods should stay allowed (it does not need pods/log), failed=%+v", pods.Failed)
	}
}

// TestInspectIdentitySoftFails confirms a failing SelfSubjectReview does not
// abort Inspect: the access reviews still run and identity is blank.
func TestInspectIdentitySoftFails(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the server could not find the requested resource")
	})
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview)
		out := sar.DeepCopy()
		out.Status.Allowed = true
		return true, out, nil
	})

	report, err := inspect(context.Background(), cs, catalog)
	if err != nil {
		t.Fatalf("inspect should not fail when identity review fails: %v", err)
	}
	if report.Identity.Username != "" {
		t.Errorf("expected blank identity, got %q", report.Identity.Username)
	}
	if len(report.Capabilities) == 0 {
		t.Error("access reviews should still run when identity soft-fails")
	}
}

// TestInspectAccessReviewTransportError confirms a transport error from an
// access review aborts the whole Inspect.
func TestInspectAccessReviewTransportError(t *testing.T) {
	cs := fakeClientset(authnv1.UserInfo{Username: "x"}, func(authzv1.ResourceAttributes) bool { return true })
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	if _, err := inspect(context.Background(), cs, catalog); err == nil {
		t.Fatal("expected error when an access review transport-fails")
	}
}

// TestCatalogShape guards the catalog invariants downstream code relies on:
// current capabilities are ordered first, and the expected current keys exist.
func TestCatalogShape(t *testing.T) {
	cat := Catalog()
	seenFuture := false
	for _, c := range cat {
		if c.Future {
			seenFuture = true
		} else if seenFuture {
			t.Errorf("current capability %q appears after a future one; order current-first", c.Key)
		}
		if len(c.Rules) == 0 {
			t.Errorf("capability %q has no rules", c.Key)
		}
	}

	want := []string{
		"view_nodes", "view_namespaces", "view_pods", "view_pod_logs",
		"restart_workloads", "scale_workloads", "manage_helm_releases",
	}
	for _, key := range want {
		found := false
		for _, c := range cat {
			if c.Key == key {
				found = true
				if c.Future {
					t.Errorf("capability %q should be a current (checked) capability", key)
				}
			}
		}
		if !found {
			t.Errorf("catalog missing current capability %q", key)
		}
	}
}
