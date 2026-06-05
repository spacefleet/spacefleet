package api

import (
	"context"

	"net/http"

	"github.com/spacefleet/spacefleet/ent"
)

// ListDeployments returns an application's rollout history (newest first). Read
// access (viewer or above), like GetApplication.
func (s *Server) ListDeployments(ctx context.Context, req ListDeploymentsRequestObject) (ListDeploymentsResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListDeploymentsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	// Confirm the application is in this org first, so the history of an app the
	// caller can't see surfaces as a 404, not an empty list.
	if _, err := s.applications.Get(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[ListDeploymentsdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	list, err := s.applications.ListDeployments(ctx, orgID, req.Id)
	if err != nil {
		return nil, err
	}
	out := make([]Deployment, len(list))
	for i, d := range list {
		out[i] = toAPIDeployment(d)
	}
	return ListDeployments200JSONResponse(out), nil
}

// GetDeployment returns one rollout run with its captured logs.
func (s *Server) GetDeployment(ctx context.Context, req GetDeploymentRequestObject) (GetDeploymentResponseObject, error) {
	orgID, aerr, err := s.resolveApp(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetDeploymentdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	d, err := s.applications.GetDeployment(ctx, orgID, req.Id, req.DeploymentId)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetDeploymentdefaultJSONResponse](http.StatusNotFound, "not_found", "deployment not found"), nil
		}
		return nil, err
	}
	return GetDeployment200JSONResponse(toAPIDeploymentDetail(d)), nil
}

// toAPIDeployment maps an ent deployment row to the API list type (no logs).
func toAPIDeployment(d *ent.Deployment) Deployment {
	out := Deployment{
		Id:             d.ID,
		ApplicationId:  d.ApplicationID,
		Action:         DeploymentAction(d.Action),
		Status:         DeploymentStatus(d.Status),
		Message:        optStr(d.Message),
		RunName:        optStr(d.RunName),
		ChartRevision:  optStr(d.ChartRevision),
		ValuesRevision: optStr(d.ValuesRevision),
		CreatedAt:      d.CreatedAt,
		FinishedAt:     d.FinishedAt,
	}
	return out
}

// toAPIDeploymentDetail is toAPIDeployment plus the captured logs.
func toAPIDeploymentDetail(d *ent.Deployment) DeploymentDetail {
	b := toAPIDeployment(d)
	return DeploymentDetail{
		Id:             b.Id,
		ApplicationId:  b.ApplicationId,
		Action:         b.Action,
		Status:         b.Status,
		Message:        b.Message,
		RunName:        b.RunName,
		ChartRevision:  b.ChartRevision,
		ValuesRevision: b.ValuesRevision,
		CreatedAt:      b.CreatedAt,
		FinishedAt:     b.FinishedAt,
		Logs:           optStr(d.Logs),
	}
}
