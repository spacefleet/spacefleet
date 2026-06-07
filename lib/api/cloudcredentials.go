package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// resolveCloudCredentialsRead runs the read preamble for cloud-credential
// handlers: confirm the service exists, then resolve + authorize org membership.
// Mirrors resolveChartCredentialsRead.
func (s *Server) resolveCloudCredentialsRead(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.cloudCredentials == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "cloud credentials service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveCloudCredentialsWrite is the read preamble plus an editor-or-above gate,
// for the handlers that change state (create, update, delete).
func (s *Server) resolveCloudCredentialsWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.cloudCredentials == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "cloud credentials service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	if aerr := requireRole(m, membership.RoleEditor); aerr != nil {
		return uuid.Nil, aerr, nil
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListCloudCredentials(ctx context.Context, _ ListCloudCredentialsRequestObject) (ListCloudCredentialsResponseObject, error) {
	orgID, aerr, err := s.resolveCloudCredentialsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListCloudCredentialsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.cloudCredentials.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]CloudCredential, len(list))
	for i, c := range list {
		out[i] = toAPICloudCredential(c)
	}
	return ListCloudCredentials200JSONResponse(out), nil
}

func (s *Server) GetCloudCredential(ctx context.Context, req GetCloudCredentialRequestObject) (GetCloudCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveCloudCredentialsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetCloudCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	c, err := s.cloudCredentials.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetCloudCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "cloud credential not found"), nil
		}
		return nil, err
	}
	return GetCloudCredential200JSONResponse(toAPICloudCredential(c)), nil
}

func (s *Server) CreateCloudCredential(ctx context.Context, req CreateCloudCredentialRequestObject) (CreateCloudCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveCloudCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateCloudCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return errResp[CreateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	provider := cloudcredential.Provider(req.Body.Provider)
	config, creds, verr := buildCloudCredential(provider, ccFieldsFromCreate(req.Body))
	if verr != nil {
		return errResp[CreateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", verr.Error()), nil
	}
	c, err := s.cloudCredentials.Create(ctx, orgID, cloudcredentials.CreateParams{
		Name:        name,
		Description: deref(req.Body.Description),
		Provider:    provider,
		Config:      config,
		Credentials: creds,
	})
	if err != nil {
		if resp, ok := cloudCredentialWriteError[CreateCloudCredentialdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateCloudCredential201JSONResponse(toAPICloudCredential(c)), nil
}

func (s *Server) UpdateCloudCredential(ctx context.Context, req UpdateCloudCredentialRequestObject) (UpdateCloudCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveCloudCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateCloudCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	// Resolve the existing record first: the provider is fixed at registration,
	// so a credential rotation must be validated against the stored provider.
	existing, err := s.cloudCredentials.Get(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateCloudCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "cloud credential not found"), nil
		}
		return nil, err
	}

	params := cloudcredentials.UpdateParams{
		Description: req.Body.Description,
	}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" {
			return errResp[UpdateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name cannot be empty"), nil
		}
		params.Name = &name
	}
	if credentialSupplied(req.Body) {
		config, creds, verr := buildCloudCredential(existing.Provider, ccFieldsFromUpdate(req.Body))
		if verr != nil {
			return errResp[UpdateCloudCredentialdefaultJSONResponse](http.StatusBadRequest, "bad_request", verr.Error()), nil
		}
		params.Config = &config
		params.Credentials = creds
	}

	c, err := s.cloudCredentials.Update(ctx, orgID, req.Id, params)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateCloudCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "cloud credential not found"), nil
		}
		if resp, ok := cloudCredentialWriteError[UpdateCloudCredentialdefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateCloudCredential200JSONResponse(toAPICloudCredential(c)), nil
}

func (s *Server) DeleteCloudCredential(ctx context.Context, req DeleteCloudCredentialRequestObject) (DeleteCloudCredentialResponseObject, error) {
	orgID, aerr, err := s.resolveCloudCredentialsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteCloudCredentialdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.cloudCredentials.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteCloudCredentialdefaultJSONResponse](http.StatusNotFound, "not_found", "cloud credential not found"), nil
		}
		return nil, err
	}
	return DeleteCloudCredential204Response{}, nil
}

// cloudCredentialWriteError maps service-layer write errors common to
// create/update to a typed client error, or reports false to bubble up as 500.
func cloudCredentialWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case cloudcredentials.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case errors.Is(err, secrets.ErrDisabled):
		return errResp[T](http.StatusBadRequest, "encryption_unavailable", "cannot store a cloud credential without an encryption key — set SPACEFLEET_SECRET_KEY"), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "a cloud credential with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

// toAPICloudCredential maps an ent row to the API type. It never exposes the
// sealed credential blob — only the name, provider, description and non-secret
// config.
func toAPICloudCredential(c *ent.CloudCredential) CloudCredential {
	out := CloudCredential{
		Id:          c.ID,
		Name:        c.Name,
		Provider:    CloudProvider(c.Provider),
		Description: optStr(c.Description),
		Config:      c.Config,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	return out
}
