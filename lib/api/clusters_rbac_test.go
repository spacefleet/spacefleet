package api

import (
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/k8s"
)

// TestRulesForCapabilities checks the catalog lookup unions known capabilities'
// full rule sets and reports unknown keys for the handler to reject.
func TestRulesForCapabilities(t *testing.T) {
	rules, unknown := rulesForCapabilities([]string{"view_pods", "restart_workloads"})
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
	// view_pods contributes pods list/watch; restart_workloads adds the apps
	// controllers + pods delete — so the union spans more than one rule.
	if len(rules) < 2 {
		t.Fatalf("rules = %d, want the union of both capabilities", len(rules))
	}

	_, unknown = rulesForCapabilities([]string{"view_pods", "not_a_capability", "also_bogus"})
	if len(unknown) != 2 {
		t.Fatalf("unknown = %v, want the two bogus keys", unknown)
	}
}

// TestRuleLines renders rules as ClusterRole entries, unioning verbs per
// resource and sorting them for stable output.
func TestRuleLines(t *testing.T) {
	if got := ruleLines(nil); got != "" {
		t.Fatalf("ruleLines(nil) = %q, want empty", got)
	}

	rules := []k8s.Rule{
		{APIGroup: "apps", Resource: "deployments", Verbs: []string{"get", "patch"}},
		// A second rule for the same group+resource: verbs must union, not duplicate.
		{APIGroup: "apps", Resource: "deployments", Verbs: []string{"create", "delete"}},
		{APIGroup: "", Resource: "pods", Subresource: "log", Verbs: []string{"get"}},
	}
	got := ruleLines(rules)

	// One block per group+resource: deployments (unioned) and pods/log.
	if n := strings.Count(got, "- apiGroups:"); n != 2 {
		t.Fatalf("blocks = %d, want 2:\n%s", n, got)
	}
	// Verbs unioned and lexically sorted.
	if !strings.Contains(got, `verbs: ["create", "delete", "get", "patch"]`) {
		t.Fatalf("deployment verbs not unioned/sorted:\n%s", got)
	}
	// Subresource is joined to the resource; the core group renders as "".
	if !strings.Contains(got, `apiGroups: [""]`) || !strings.Contains(got, `resources: ["pods/log"]`) {
		t.Fatalf("core/subresource not rendered:\n%s", got)
	}
}

// TestRbacForRules covers the method branches: a ServiceAccount identity yields
// a bound manifest, a kubeconfig method yields guidance, and an empty rule set
// yields nothing.
func TestRbacForRules(t *testing.T) {
	rules := []k8s.Rule{{APIGroup: "", Resource: "pods", Verbs: []string{"list"}}}
	sa := k8s.Identity{Username: "system:serviceaccount:spacefleet:reader"}

	manifest := rbacForRules(k8s.MethodInCluster, sa, rules)
	for _, want := range []string{
		"kind: ClusterRole",
		"kind: ClusterRoleBinding",
		"name: spacefleet-access",
		"kind: ServiceAccount",
		"name: reader",
		"namespace: spacefleet",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("in_cluster manifest missing %q:\n%s", want, manifest)
		}
	}

	// A non-ServiceAccount subject binds a User.
	userManifest := rbacForRules(k8s.MethodToken, k8s.Identity{Username: "alice"}, rules)
	if !strings.Contains(userManifest, "kind: User") || !strings.Contains(userManifest, "name: alice") {
		t.Fatalf("token manifest should bind a User:\n%s", userManifest)
	}

	// Methods without an in-cluster subject return guidance, not a manifest.
	guidance := rbacForRules(k8s.MethodKubeconfig, sa, rules)
	if strings.Contains(guidance, "kind: ClusterRoleBinding") {
		t.Fatalf("kubeconfig should return guidance, not a binding:\n%s", guidance)
	}
	if !strings.Contains(guidance, "kubeconfig") {
		t.Fatalf("kubeconfig guidance missing its hint:\n%s", guidance)
	}

	if got := rbacForRules(k8s.MethodInCluster, sa, nil); got != "" {
		t.Fatalf("empty rules = %q, want empty", got)
	}
}
