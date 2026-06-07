package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/githubinstallations"
)

// resolveGitHubInstallationsRead runs the read preamble: confirm the service
// exists, then resolve + authorize org membership. Mirrors the chart-credentials
// preamble.
func (s *Server) resolveGitHubInstallationsRead(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.githubInstallations == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "github installations service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveGitHubInstallationsWrite is the read preamble plus an editor-or-above
// gate, for the connect/create/delete handlers that change state.
func (s *Server) resolveGitHubInstallationsWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.githubInstallations == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "github installations service not configured"}, nil
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

func (s *Server) ListGitHubInstallations(ctx context.Context, _ ListGitHubInstallationsRequestObject) (ListGitHubInstallationsResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListGitHubInstallationsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.githubInstallations.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]GitHubInstallation, len(list))
	for i, inst := range list {
		out[i] = toAPIGitHubInstallation(inst)
	}
	return ListGitHubInstallations200JSONResponse(out), nil
}

func (s *Server) ListGitHubRepositories(ctx context.Context, _ ListGitHubRepositoriesRequestObject) (ListGitHubRepositoriesResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListGitHubRepositoriesdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	repos, err := s.githubInstallations.ListRepositories(ctx, orgID)
	if err != nil {
		if errors.Is(err, githubinstallations.ErrAppNotConfigured) {
			return errResp[ListGitHubRepositoriesdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "github app is not configured on this deployment"), nil
		}
		return nil, err
	}
	out := make([]GitHubRepository, len(repos))
	for i, r := range repos {
		out[i] = toAPIGitHubRepository(r)
	}
	return ListGitHubRepositories200JSONResponse(out), nil
}

// GetGitHubConnectUrl returns the GitHub App install URL to redirect the browser
// to, carrying a signed state token that binds the connect flow to this org.
func (s *Server) GetGitHubConnectUrl(ctx context.Context, _ GetGitHubConnectUrlRequestObject) (GetGitHubConnectUrlResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetGitHubConnectUrldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.githubAppSlug == "" {
		return errResp[GetGitHubConnectUrldefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "github app is not configured on this deployment"), nil
	}
	if s.secretKey == "" {
		return errResp[GetGitHubConnectUrldefaultJSONResponse](http.StatusBadRequest, "encryption_unavailable", "cannot sign the connect flow without an encryption key — set SPACEFLEET_SECRET_KEY"), nil
	}
	state, err := githubapp.SignState(s.secretKey, orgID)
	if err != nil {
		return nil, err
	}
	// Known limitation: the github.com base URL is hardcoded — GitHub Enterprise
	// Server (GHES) installs at a self-hosted host and is not yet supported. The
	// GHES base URL is not configurable here.
	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s",
		url.PathEscape(s.githubAppSlug), url.QueryEscape(state))
	return GetGitHubConnectUrl200JSONResponse{Url: installURL}, nil
}

// CreateGitHubInstallation records an installation from the connect callback. It
// verifies the state token (signature, expiry, and that it was issued for the
// current org) before recording, so a caller can't attach an installation id it
// doesn't own.
func (s *Server) CreateGitHubInstallation(ctx context.Context, req CreateGitHubInstallationRequestObject) (CreateGitHubInstallationResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	if s.secretKey == "" {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "encryption_unavailable", "cannot verify the connect flow without an encryption key — set SPACEFLEET_SECRET_KEY"), nil
	}
	stateOrg, err := githubapp.VerifyState(s.secretKey, req.Body.State)
	if err != nil {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "invalid or expired connect state; start the GitHub connection again"), nil
	}
	if stateOrg != orgID {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusForbidden, "forbidden", "connect state was issued for a different organization"), nil
	}
	inst, err := s.githubInstallations.Link(ctx, orgID, req.Body.InstallationId)
	if err != nil {
		if errors.Is(err, githubinstallations.ErrAppNotConfigured) {
			return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "github app is not configured on this deployment"), nil
		}
		// A failure to confirm the installation against GitHub (bad id, App lacks
		// access, GitHub unreachable) is a bad-gateway-ish client-correctable
		// error rather than an internal fault.
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadGateway, "github_error", "could not verify the installation with GitHub: "+err.Error()), nil
	}
	return CreateGitHubInstallation201JSONResponse(toAPIGitHubInstallation(inst)), nil
}

func (s *Server) DeleteGitHubInstallation(ctx context.Context, req DeleteGitHubInstallationRequestObject) (DeleteGitHubInstallationResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteGitHubInstallationdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.githubInstallations.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteGitHubInstallationdefaultJSONResponse](http.StatusNotFound, "not_found", "github installation not found"), nil
		}
		// The FK from components is ON DELETE RESTRICT: an installation in use
		// can't be deleted (the service classifies the DB violation as ErrInUse).
		if errors.Is(err, githubinstallations.ErrInUse) {
			return errResp[DeleteGitHubInstallationdefaultJSONResponse](http.StatusConflict, "conflict", "this installation is attached to a workflow component; detach it first"), nil
		}
		return nil, err
	}
	return DeleteGitHubInstallation204Response{}, nil
}

// toAPIGitHubInstallation maps an ent row to the API type. No secret to omit —
// the row carries only the installation id and the account it is installed on.
func toAPIGitHubInstallation(inst *ent.GitHubInstallation) GitHubInstallation {
	return GitHubInstallation{
		Id:             inst.ID,
		InstallationId: inst.InstallationID,
		AccountLogin:   optStr(inst.AccountLogin),
		AccountType:    optStr(inst.AccountType),
		CreatedAt:      inst.CreatedAt,
		UpdatedAt:      inst.UpdatedAt,
	}
}

// toAPIGitHubRepository maps an aggregated repository to the API type. account
// and default branch are optional in the contract, so map them through optStr.
func toAPIGitHubRepository(r githubinstallations.Repository) GitHubRepository {
	return GitHubRepository{
		InstallationId: r.InstallationID,
		AccountLogin:   optStr(r.AccountLogin),
		FullName:       r.FullName,
		CloneUrl:       r.CloneURL,
		DefaultBranch:  optStr(r.DefaultBranch),
		Private:        r.Private,
	}
}
