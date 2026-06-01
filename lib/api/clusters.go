package api

import (
	"context"
	"errors"
	"net/http"
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
