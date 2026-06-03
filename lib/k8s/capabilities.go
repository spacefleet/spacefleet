package k8s

import (
	"context"
	"fmt"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Rule is a single RBAC permission Spacefleet needs, modeled on an
// authzv1.ResourceAttributes. Each verb is checked independently (one
// SelfSubjectAccessReview per verb).
type Rule struct {
	// APIGroup is the resource's API group; "" for the core group. It maps to
	// authzv1.ResourceAttributes.Group.
	APIGroup string
	// Resource is the resource type, e.g. "pods", "nodes", "namespaces".
	Resource string
	// Subresource is the subresource, e.g. "log" for pods/log; "" otherwise.
	Subresource string
	// Verbs are the verbs required, e.g. {"list","watch"}. Each is checked with
	// its own access review.
	Verbs []string
	// ClusterScoped reports whether the permission is checked cluster-wide
	// (empty namespace in the review). All of Spacefleet's current readers are
	// cluster-wide (Nodes, Namespaces, NamespaceAll pods, pods/log), so this is
	// true for them.
	ClusterScoped bool
}

// Capability groups the RBAC rules behind one product feature so the report can
// say "View pods: allowed/denied" rather than leaking individual verbs.
type Capability struct {
	// Key is a stable identifier, e.g. "view_nodes".
	Key string
	// Name is a human-readable label, e.g. "View nodes".
	Name string
	// Future marks a capability that is declared in the catalog but NOT checked
	// by Inspect — a placeholder for a feature that isn't wired yet.
	Future bool
	// Rules are the permissions the capability requires; it is "allowed" only
	// when every rule/verb is allowed.
	Rules []Rule
}

// catalog is the declarative source of truth for the capabilities Spacefleet
// can report on, ordered by product area: the read features the
// nodes/namespaces/pods/logs readers call (Observe), then the write operations
// upcoming features need (Operate, Deploy). Every entry here is checked; the
// Future flag (see Capability.Future) lets a not-yet-wired capability be
// declared without being checked, but nothing in the catalog uses it today.
var catalog = []Capability{
	{
		Key:  "view_nodes",
		Name: "View nodes",
		Rules: []Rule{
			{APIGroup: "", Resource: "nodes", Verbs: []string{"list", "watch"}, ClusterScoped: true},
		},
	},
	{
		Key:  "view_namespaces",
		Name: "View namespaces",
		Rules: []Rule{
			{APIGroup: "", Resource: "namespaces", Verbs: []string{"list", "watch"}, ClusterScoped: true},
		},
	},
	{
		Key:  "view_pods",
		Name: "View pods",
		// ListPods uses NamespaceAll, so the view is a cluster-wide list.
		Rules: []Rule{
			{APIGroup: "", Resource: "pods", Verbs: []string{"list", "watch"}, ClusterScoped: true},
		},
	},
	{
		Key:  "view_pod_logs",
		Name: "View pod logs",
		// StreamPodLogs reads the pods/log subresource with a get.
		Rules: []Rule{
			{APIGroup: "", Resource: "pods", Subresource: "log", Verbs: []string{"get"}, ClusterScoped: true},
		},
	},
	// Write capabilities. These are CHECKED (not placeholders): the report shows
	// whether a cluster's credentials can perform the write operations upcoming
	// features need, and the remediation tells the operator exactly how to grant
	// each one. Verbs are kept to the minimum each operation actually uses, and
	// the checks are cluster-wide (empty namespace) to mirror the cluster-wide
	// grant the remediation produces.
	{
		Key:  "restart_workloads",
		Name: "Restart workloads",
		// A rollout restart patches the controller's pod-template annotation
		// (get + patch); deleting a pod is the lower-level recreate path.
		Rules: []Rule{
			{APIGroup: "apps", Resource: "deployments", Verbs: []string{"get", "patch"}, ClusterScoped: true},
			{APIGroup: "apps", Resource: "statefulsets", Verbs: []string{"get", "patch"}, ClusterScoped: true},
			{APIGroup: "apps", Resource: "daemonsets", Verbs: []string{"get", "patch"}, ClusterScoped: true},
			{APIGroup: "", Resource: "pods", Verbs: []string{"delete"}, ClusterScoped: true},
		},
	},
	{
		Key:  "scale_workloads",
		Name: "Scale workloads",
		// Scaling goes through the scale subresource, not the workload object.
		Rules: []Rule{
			{APIGroup: "apps", Resource: "deployments", Subresource: "scale", Verbs: []string{"get", "update"}, ClusterScoped: true},
			{APIGroup: "apps", Resource: "statefulsets", Subresource: "scale", Verbs: []string{"get", "update"}, ClusterScoped: true},
		},
	},
	{
		Key:  "manage_helm_releases",
		Name: "Manage Helm releases",
		// Helm stores release state as Secrets and creates/updates/removes the
		// resources a chart renders. This is a REPRESENTATIVE least-privilege set
		// (release storage + the common core/apps kinds), not the full union of
		// what every chart may touch — a chart with CRDs or RBAC objects needs
		// more. It is enough to tell an operator whether basic Helm installs work.
		Rules: []Rule{
			{APIGroup: "", Resource: "secrets", Verbs: []string{"get", "list", "create", "update", "delete"}, ClusterScoped: true},
			{APIGroup: "", Resource: "configmaps", Verbs: []string{"create", "update", "delete"}, ClusterScoped: true},
			{APIGroup: "", Resource: "services", Verbs: []string{"create", "update", "delete"}, ClusterScoped: true},
			{APIGroup: "apps", Resource: "deployments", Verbs: []string{"create", "update", "delete"}, ClusterScoped: true},
		},
	},
	{
		Key:  "run_jobs",
		Name: "Run jobs (Tekton)",
		// The RUN path only: submit TaskRuns/PipelineRuns and read their pod logs.
		// Installing Tekton itself needs cluster-admin-equivalent rights (CRDs,
		// cluster RBAC, webhooks) and is NOT modeled as a least-privilege
		// capability — it would just be "*/*". The install worker surfaces a
		// forbidden error directly if the credentials can't install; this
		// capability reports whether, once Tekton is present, the credentials can
		// actually drive it.
		Rules: []Rule{
			{APIGroup: "tekton.dev", Resource: "taskruns", Verbs: []string{"create", "get", "list", "watch"}, ClusterScoped: true},
			{APIGroup: "tekton.dev", Resource: "pipelineruns", Verbs: []string{"create", "get", "list", "watch"}, ClusterScoped: true},
			{APIGroup: "", Resource: "pods", Verbs: []string{"get", "list"}, ClusterScoped: true},
			{APIGroup: "", Resource: "pods", Subresource: "log", Verbs: []string{"get"}, ClusterScoped: true},
		},
	},
}

// Catalog returns the declarative capability catalog (current features first,
// then future placeholders). The returned slice shares backing storage with the
// package var; callers must not mutate it.
func Catalog() []Capability {
	return catalog
}

// Identity is the resolved caller identity the cluster's credentials map to, as
// reported by a SelfSubjectReview. Storage-agnostic plain Go, like Node/Pod.
type Identity struct {
	// Username is the authenticated user, e.g. "system:serviceaccount:ns:name".
	Username string
	UID      string
	Groups   []string
}

// RuleResult is the outcome of checking one verb of one rule.
type RuleResult struct {
	Rule    Rule
	Verb    string
	Allowed bool
	// Reason is the authorizer's explanation (SubjectAccessReviewStatus.Reason).
	Reason string
	// Error is the authorizer's evaluation error, if any.
	Error string
}

// CapabilityResult is a capability's aggregate verdict: allowed only when every
// checked rule/verb was allowed, with the denials collected in Failed.
type CapabilityResult struct {
	Key     string
	Name    string
	Allowed bool
	// Failed holds the rule/verb results that were denied (empty when allowed).
	Failed []RuleResult
}

// CapabilityReport is Inspect's storage-agnostic output: the resolved identity
// plus a per-capability allow/deny verdict. It deliberately carries no client-go
// types and no credentials so the API layer can map it like Node/Pod.
type CapabilityReport struct {
	Identity     Identity
	Capabilities []CapabilityResult
}

// Inspect resolves the caller identity the connection's credentials map to and
// checks, for every CURRENT capability in the catalog, whether the caller is
// allowed each required verb. It builds the client the same way the List*
// readers do (RESTConfig -> NewForConfig) and bounds the whole sequence by
// listTimeout. A transport failure (unreachable API server, denied access
// review) is returned to the caller; the cluster row is not touched.
//
// Future capabilities are declared in the catalog but skipped here — they are
// placeholders for features that aren't wired yet.
func Inspect(ctx context.Context, conn Connection) (*CapabilityReport, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	return inspect(ctx, cs, catalog)
}

// ResolveIdentity builds a client from the connection and resolves only the
// caller identity its credentials map to (one SelfSubjectReview, no access
// reviews). It is the lightweight counterpart to Inspect for callers that need
// just the subject — e.g. to fill the binding of a generated RBAC manifest.
// Identity resolution itself soft-fails to a blank Identity against older API
// servers (see resolveIdentity); only building the client is returned as an
// error.
func ResolveIdentity(ctx context.Context, conn Connection) (Identity, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return Identity{}, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Identity{}, fmt.Errorf("k8s: client: %w", err)
	}
	return resolveIdentity(ctx, cs), nil
}

// inspect runs the identity resolution and access reviews against an already
// built clientset. It is split out from Inspect so tests can drive it with a
// fake clientset.
func inspect(ctx context.Context, cs kubernetes.Interface, caps []Capability) (*CapabilityReport, error) {
	report := &CapabilityReport{Identity: resolveIdentity(ctx, cs)}

	for _, cap := range caps {
		if cap.Future {
			continue
		}
		result := CapabilityResult{Key: cap.Key, Name: cap.Name, Allowed: true}
		for _, rule := range cap.Rules {
			for _, verb := range rule.Verbs {
				rr, err := reviewAccess(ctx, cs, rule, verb)
				if err != nil {
					return nil, err
				}
				if !rr.Allowed {
					result.Allowed = false
					result.Failed = append(result.Failed, rr)
				}
			}
		}
		report.Capabilities = append(report.Capabilities, result)
	}
	return report, nil
}

// resolveIdentity reads the caller identity via a SelfSubjectReview. The review
// graduated to GA in Kubernetes 1.28; against older API servers the call can
// fail, so we soft-fail (return a blank Identity) rather than abort — the access
// reviews are the important signal and still run.
func resolveIdentity(ctx context.Context, cs kubernetes.Interface) Identity {
	res, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return Identity{}
	}
	u := res.Status.UserInfo
	return Identity{Username: u.Username, UID: u.UID, Groups: u.Groups}
}

// reviewAccess issues one SelfSubjectAccessReview for a single rule/verb. A
// transport error is returned (it aborts the whole Inspect); an authorizer
// denial is a normal not-allowed result.
func reviewAccess(ctx context.Context, cs kubernetes.Interface, rule Rule, verb string) (RuleResult, error) {
	attrs := &authzv1.ResourceAttributes{
		Group:       rule.APIGroup,
		Resource:    rule.Resource,
		Subresource: rule.Subresource,
		Verb:        verb,
	}
	// A cluster-scoped check leaves Namespace empty, matching the cluster-wide
	// usage of the current readers.
	if !rule.ClusterScoped {
		attrs.Namespace = ""
	}
	sar := &authzv1.SelfSubjectAccessReview{
		Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
	}
	out, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return RuleResult{}, fmt.Errorf("k8s: access review: %w", err)
	}
	return RuleResult{
		Rule:    rule,
		Verb:    verb,
		Allowed: out.Status.Allowed,
		Reason:  out.Status.Reason,
		Error:   out.Status.EvaluationError,
	}, nil
}
