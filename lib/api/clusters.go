package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// apiError is a resolved client-facing error (status + body fields) that a
// handler renders into its operation-specific typed default response.
type apiError struct {
	status int
	code   string
	msg    string
}

// resolveOrg runs the common preamble for every org-scoped cluster handler:
// confirm the services exist, resolve the authenticated user, and resolve +
// authorize the target organization. It returns (orgID, nil, nil) on success,
// (_, *apiError, nil) for a client error to render, or (_, nil, err) for an
// internal error to bubble up.
func (s *Server) resolveOrg(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.clusters == nil || s.users == nil || s.orgs == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "clusters service not configured"}, nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return uuid.Nil, &apiError{http.StatusUnauthorized, "unauthorized", "no session"}, nil
		}
		return uuid.Nil, nil, err
	}
	m, err := s.currentOrg(ctx, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, errNoOrg):
			return uuid.Nil, &apiError{http.StatusBadRequest, "bad_request", "no organization selected"}, nil
		case ent.IsNotFound(err):
			return uuid.Nil, &apiError{http.StatusForbidden, "forbidden", "not a member of this organization"}, nil
		default:
			return uuid.Nil, nil, err
		}
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListClusters(ctx context.Context, _ ListClustersRequestObject) (ListClustersResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClustersdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.clusters.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, len(list))
	for i, c := range list {
		out[i] = toAPICluster(c)
	}
	return ListClusters200JSONResponse(out), nil
}

func (s *Server) GetCluster(ctx context.Context, req GetClusterRequestObject) (GetClusterResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetClusterdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	c, err := s.clusters.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetClusterdefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}
	return GetCluster200JSONResponse(toAPICluster(c)), nil
}

func (s *Server) CreateCluster(ctx context.Context, req CreateClusterRequestObject) (CreateClusterResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateClusterdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	conn, verr := buildConnection(req.Body.ConnectionMethod, fieldsFromCreate(req.Body))
	if verr != nil {
		return errResp[CreateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", verr.Error()), nil
	}
	c, err := s.clusters.Create(ctx, orgID, clusters.CreateParams{
		Name:            name,
		Method:          k8s.Method(req.Body.ConnectionMethod),
		ConnectionInput: conn,
	})
	if err != nil {
		if resp, ok := clusterWriteError[CreateClusterdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateCluster201JSONResponse(toAPICluster(c)), nil
}

func (s *Server) UpdateCluster(ctx context.Context, req UpdateClusterRequestObject) (UpdateClusterResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateClusterdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	// Look up the existing cluster first: its (immutable) connection method
	// drives validation when credentials are re-supplied.
	existing, err := s.clusters.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateClusterdefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}

	params := clusters.UpdateParams{}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	if connectionSupplied(req.Body) {
		conn, verr := buildConnection(ConnectionMethod(existing.ConnectionMethod), fieldsFromUpdate(req.Body))
		if verr != nil {
			return errResp[UpdateClusterdefaultJSONResponse](http.StatusBadRequest, "bad_request", verr.Error()), nil
		}
		params.Connection = &conn
	}

	c, err := s.clusters.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if resp, ok := clusterWriteError[UpdateClusterdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateCluster200JSONResponse(toAPICluster(c)), nil
}

func (s *Server) DeleteCluster(ctx context.Context, req DeleteClusterRequestObject) (DeleteClusterResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteClusterdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.clusters.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteClusterdefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}
	return DeleteCluster204Response{}, nil
}

func (s *Server) TestCluster(ctx context.Context, req TestClusterRequestObject) (TestClusterResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[TestClusterdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	c, err := s.clusters.Test(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[TestClusterdefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}
	return TestCluster200JSONResponse(toAPICluster(c)), nil
}

func (s *Server) ListClusterNodes(ctx context.Context, req ListClusterNodesRequestObject) (ListClusterNodesResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClusterNodesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	nodes, err := s.clusters.Nodes(ctx, orgID, req.Id)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		return errResp[ListClusterNodesdefaultJSONResponse](status, code, msg), nil
	}
	return ListClusterNodes200JSONResponse(toAPINodes(nodes)), nil
}

func (s *Server) ListClusterCapabilities(ctx context.Context, req ListClusterCapabilitiesRequestObject) (ListClusterCapabilitiesResponseObject, error) {
	orgID, aerr, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListClusterCapabilitiesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	// Resolve the cluster first: its (immutable) connection method tailors the
	// remediation guidance, and a not-found short-circuits before any live call.
	c, err := s.clusters.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[ListClusterCapabilitiesdefaultJSONResponse](http.StatusNotFound, "not_found", "cluster not found"), nil
		}
		return nil, err
	}
	report, err := s.clusters.Capabilities(ctx, orgID, req.Id)
	if err != nil {
		status, code, msg := nodesFetchError(err)
		return errResp[ListClusterCapabilitiesdefaultJSONResponse](status, code, msg), nil
	}
	return ListClusterCapabilities200JSONResponse(toAPICapabilities(report, k8s.Method(c.ConnectionMethod))), nil
}

// nodesFetchError classifies a node list/watch failure into a client-facing
// status. The distinction matters for the live stream: the client retries
// transient failures (an unreachable cluster may come back) but stops on
// terminal ones. A Kubernetes authorization denial (e.g. the cluster's stored
// credentials lack RBAC to list nodes) won't fix itself on retry, so it's
// reported as a terminal 403 rather than a retriable 502.
func nodesFetchError(err error) (status int, code, msg string) {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound, "not_found", "cluster not found"
	case errors.Is(err, secrets.ErrDisabled):
		return http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return http.StatusForbidden, "cluster_forbidden", "the cluster's credentials are not authorized to list nodes: " + err.Error()
	default:
		// Building the client or reaching the API server failed: the request is
		// well-formed, but the upstream cluster is (currently) unreachable.
		return http.StatusBadGateway, "cluster_unreachable", err.Error()
	}
}

func toAPINode(n k8s.Node) Node {
	out := Node{
		Name:             n.Name,
		Ready:            n.Ready,
		Unschedulable:    n.Unschedulable,
		Roles:            n.Roles,
		Labels:           n.Labels,
		Architecture:     optStr(n.Architecture),
		ContainerRuntime: optStr(n.ContainerRuntime),
		ExternalIp:       optStr(n.ExternalIP),
		Hostname:         optStr(n.Hostname),
		InstanceType:     optStr(n.InstanceType),
		InternalIp:       optStr(n.InternalIP),
		KernelVersion:    optStr(n.KernelVersion),
		KubeletVersion:   optStr(n.KubeletVersion),
		OperatingSystem:  optStr(n.OperatingSystem),
		OsImage:          optStr(n.OSImage),
		PodCidr:          optStr(n.PodCIDR),
		ProviderId:       optStr(n.ProviderID),
		Region:           optStr(n.Region),
		Zone:             optStr(n.Zone),
		CreatedAt:        n.CreatedAt,
		Capacity:         &NodeResources{Cpu: n.Capacity.CPU, Memory: n.Capacity.Memory, Pods: n.Capacity.Pods},
		Allocatable:      &NodeResources{Cpu: n.Allocatable.CPU, Memory: n.Allocatable.Memory, Pods: n.Allocatable.Pods},
		Taints:           make([]NodeTaint, len(n.Taints)),
		Conditions:       make([]NodeCondition, len(n.Conditions)),
	}
	if out.Roles == nil {
		out.Roles = []string{}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	for i, t := range n.Taints {
		out.Taints[i] = NodeTaint{Key: t.Key, Value: optStr(t.Value), Effect: t.Effect}
	}
	for i, c := range n.Conditions {
		nc := NodeCondition{Type: c.Type, Status: c.Status, Reason: optStr(c.Reason), Message: optStr(c.Message)}
		nc.LastTransitionTime = c.LastTransitionTime
		out.Conditions[i] = nc
	}
	return out
}

// capabilityAreas groups capabilities by product area for display. A capability
// without an entry falls back to "Other".
var capabilityAreas = map[string]string{
	"view_nodes":           "Observe",
	"view_namespaces":      "Observe",
	"view_pods":            "Observe",
	"view_pod_logs":        "Observe",
	"restart_workloads":    "Operate",
	"scale_workloads":      "Operate",
	"manage_helm_releases": "Deploy",
}

func capabilityArea(key string) string {
	if a, ok := capabilityAreas[key]; ok {
		return a
	}
	return "Other"
}

// toAPICapabilities maps the storage-agnostic k8s capability report to the API
// type. It never exposes credentials — only the resolved identity and the
// per-capability allow/deny verdict — and, when any capability is denied,
// attaches identity-aware RBAC remediation tailored to the connection method.
func toAPICapabilities(r *k8s.CapabilityReport, method k8s.Method) ClusterCapabilities {
	out := ClusterCapabilities{
		Identity: ClusterIdentity{
			Username: optStr(r.Identity.Username),
			Uid:      optStr(r.Identity.UID),
			Groups:   r.Identity.Groups,
		},
		Capabilities: make([]Capability, len(r.Capabilities)),
	}
	if out.Identity.Groups == nil {
		out.Identity.Groups = []string{}
	}

	anyDenied := false
	for i, cr := range r.Capabilities {
		cap := Capability{
			Key:          cr.Key,
			Area:         capabilityArea(cr.Key),
			Title:        cr.Name,
			Status:       Allowed,
			MissingRules: make([]CapabilityRule, 0, len(cr.Failed)),
		}
		if !cr.Allowed {
			cap.Status = Denied
			anyDenied = true
			// Per-capability remediation lets an operator enable one capability
			// at a time (e.g. grant restart without granting full Helm deploy),
			// scoped to just this capability's missing rules. Only emitted for
			// methods with an addressable in-cluster subject; otherwise the
			// report-level guidance covers the union.
			if rem := capRemediation(cr, r.Identity, method); rem != "" {
				cap.Remediation = &rem
			}
		}
		for _, f := range cr.Failed {
			cap.MissingRules = append(cap.MissingRules, CapabilityRule{
				ApiGroup:    optStr(f.Rule.APIGroup),
				Resource:    f.Rule.Resource,
				Subresource: optStr(f.Rule.Subresource),
				Verb:        f.Verb,
				Reason:      optStr(f.Reason),
			})
		}
		out.Capabilities[i] = cap
	}

	if anyDenied {
		if rem := remediation(r, method); rem != "" {
			out.Remediation = &rem
		}
	}
	return out
}

// remediation produces copy-paste guidance for granting the missing
// permissions. For methods with an addressable in-cluster subject (in_cluster,
// token) it emits a ready-to-apply ClusterRole + ClusterRoleBinding bound to the
// resolved subject. For the other methods (kubeconfig, eks, gke, aks) the
// effective subject lives outside the cluster's own RBAC subjects (an IAM/AAD
// principal or an embedded kubeconfig user), so it returns best-effort guidance
// rather than a manifest that would bind the wrong subject.
func remediation(r *k8s.CapabilityReport, method k8s.Method) string {
	var failed []k8s.RuleResult
	for _, cr := range r.Capabilities {
		failed = append(failed, cr.Failed...)
	}
	rules := ruleLinesFromFailed(failed)
	if rules == "" {
		return ""
	}
	switch method {
	case k8s.MethodInCluster, k8s.MethodToken:
		return rbacManifest("spacefleet-access", r.Identity, rules)
	default:
		return rbacGuidance(method, r.Identity, rules)
	}
}

// capRemediation builds a ready-to-apply manifest granting just one denied
// capability's missing rules, under a capability-specific role name so an
// operator can apply several without collision. It is emitted only for methods
// whose subject lives in the cluster's own RBAC (in_cluster, token); for the
// rest the report-level guidance already shows the rules for the operator's
// out-of-cluster subject, so a per-capability manifest would bind the wrong one.
func capRemediation(cr k8s.CapabilityResult, id k8s.Identity, method k8s.Method) string {
	switch method {
	case k8s.MethodInCluster, k8s.MethodToken:
	default:
		return ""
	}
	rules := ruleLinesFromFailed(cr.Failed)
	if rules == "" {
		return ""
	}
	name := "spacefleet-" + strings.ReplaceAll(cr.Key, "_", "-")
	return rbacManifest(name, id, rules)
}

// ruleLinesFromFailed renders denied rule/verbs as ClusterRole `rules:` entries
// (one block per APIGroup+resource[/subresource], verbs unioned and sorted), or
// "" when the slice is empty.
func ruleLinesFromFailed(failed []k8s.RuleResult) string {
	type ruleKey struct {
		group, resource string
	}
	order := []ruleKey{}
	verbs := map[ruleKey]map[string]bool{}
	for _, f := range failed {
		res := f.Rule.Resource
		if f.Rule.Subresource != "" {
			res = res + "/" + f.Rule.Subresource
		}
		k := ruleKey{group: f.Rule.APIGroup, resource: res}
		if _, ok := verbs[k]; !ok {
			verbs[k] = map[string]bool{}
			order = append(order, k)
		}
		verbs[k][f.Verb] = true
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range order {
		vs := sortedKeys(verbs[k])
		fmt.Fprintf(&b, "  - apiGroups: [%s]\n", yamlQuote(k.group))
		fmt.Fprintf(&b, "    resources: [%s]\n", yamlQuote(k.resource))
		b.WriteString("    verbs: [")
		for i, v := range vs {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(yamlQuote(v))
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// rbacManifest builds a ClusterRole + ClusterRoleBinding (both named roleName)
// granting rules to the resolved subject. The subject kind/name/namespace are
// parsed from the identity's username; a ServiceAccount username has the
// well-known form "system:serviceaccount:<namespace>:<name>".
func rbacManifest(roleName string, id k8s.Identity, rules string) string {
	var b strings.Builder
	b.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
	b.WriteString("kind: ClusterRole\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  name: %s\n", roleName)
	b.WriteString("rules:\n")
	b.WriteString(rules)
	b.WriteString("---\n")
	b.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
	b.WriteString("kind: ClusterRoleBinding\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  name: %s\n", roleName)
	b.WriteString("roleRef:\n")
	b.WriteString("  apiGroup: rbac.authorization.k8s.io\n")
	b.WriteString("  kind: ClusterRole\n")
	fmt.Fprintf(&b, "  name: %s\n", roleName)
	b.WriteString("subjects:\n")
	if ns, name, ok := parseServiceAccount(id.Username); ok {
		b.WriteString("  - kind: ServiceAccount\n")
		fmt.Fprintf(&b, "    name: %s\n", name)
		fmt.Fprintf(&b, "    namespace: %s\n", ns)
	} else {
		// A non-ServiceAccount subject (a user the API server authenticated): the
		// binding targets the User by name. The name may be empty if the identity
		// couldn't be resolved (older API servers) — flag it for the operator.
		name := id.Username
		if name == "" {
			name = "<the identity used by this cluster's credentials>"
		}
		b.WriteString("  - kind: User\n")
		b.WriteString("    apiGroup: rbac.authorization.k8s.io\n")
		fmt.Fprintf(&b, "    name: %s\n", name)
	}
	return b.String()
}

// rbacGuidance returns best-effort text for methods whose effective subject is
// not a plain in-cluster RBAC subject. It still shows the missing rules so the
// operator can attach them to whatever subject their auth model uses.
func rbacGuidance(method k8s.Method, id k8s.Identity, rules string) string {
	subject := id.Username
	if subject == "" {
		subject = "the identity these credentials authenticate as"
	}
	var hint string
	switch method {
	case k8s.MethodKubeconfig:
		hint = "These credentials come from a supplied kubeconfig. Grant the rules " +
			"below to the user/group that kubeconfig authenticates as, via a " +
			"ClusterRole + ClusterRoleBinding on the cluster."
	case k8s.MethodEKS:
		hint = "These credentials authenticate via AWS IAM. Map the IAM principal to " +
			"a Kubernetes subject (aws-auth / EKS access entries), then bind the rules " +
			"below to that subject with a ClusterRole + ClusterRoleBinding."
	case k8s.MethodGKE:
		hint = "These credentials authenticate via Google Cloud IAM. Bind the rules " +
			"below to the corresponding Kubernetes user/group (the GCP identity) with a " +
			"ClusterRole + ClusterRoleBinding."
	case k8s.MethodAKS:
		hint = "These credentials authenticate via Microsoft Entra ID. Bind the rules " +
			"below to the corresponding Kubernetes user/group (the Entra object ID) with " +
			"a ClusterRole + ClusterRoleBinding."
	default:
		hint = "Grant the rules below to the subject these credentials authenticate as " +
			"via a ClusterRole + ClusterRoleBinding."
	}
	return fmt.Sprintf("# %s\n# Resolved identity: %s\n#\n# Required ClusterRole rules:\n%s", hint, subject, rules)
}

// parseServiceAccount extracts (namespace, name) from a ServiceAccount username
// of the form "system:serviceaccount:<namespace>:<name>".
func parseServiceAccount(username string) (namespace, name string, ok bool) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(username, prefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// yamlQuote double-quotes a YAML scalar so the empty string (the core API group)
// and values like "*" render unambiguously inside a flow sequence.
func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// sortedKeys returns the keys of a set in lexical order for stable output.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// optStr returns a pointer to s, or nil when s is empty — so omitempty fields
// stay absent from the JSON rather than serializing as "".
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// clusterWriteError maps service-layer write errors common to create/update to
// a typed client error, or reports false to let the caller bubble it up as 500.
func clusterWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case errors.Is(err, secrets.ErrDisabled):
		return errResp[T](http.StatusBadRequest, "encryption_unavailable", "this cluster has credentials but no encryption key is configured — set SPACEFLEET_SECRET_KEY"), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "a cluster with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func toAPICluster(c *ent.Cluster) Cluster {
	out := Cluster{
		Id:               c.ID,
		Name:             c.Name,
		ConnectionMethod: ConnectionMethod(c.ConnectionMethod),
		Status:           ClusterStatus(c.Status),
		Config:           c.Config,
		LastCheckedAt:    c.LastCheckedAt,
		CreatedAt:        c.CreatedAt,
		UpdatedAt:        c.UpdatedAt,
	}
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	if c.Endpoint != "" {
		out.Endpoint = &c.Endpoint
	}
	if c.K8sVersion != "" {
		out.K8sVersion = &c.K8sVersion
	}
	if c.StatusMessage != "" {
		out.StatusMessage = &c.StatusMessage
	}
	return out
}
