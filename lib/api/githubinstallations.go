package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

// GetGitHubAuthorizeUrl returns GitHub's OAuth authorize URL for the second
// leg of the setup-URL connect flow: GitHub has just sent the browser to the
// App's setup URL with the new installation's id and the connect state, and
// the code the authorize step returns is what proves the user can access that
// installation (the setup URL's installation_id alone can be forged). The
// connect state must be this org's, so a link someone else crafted can't
// start it; the new state binds the org and the installation, and the
// redirect lands on /github/callback, which posts the code to
// CreateGitHubInstallation.
func (s *Server) GetGitHubAuthorizeUrl(ctx context.Context, req GetGitHubAuthorizeUrlRequestObject) (GetGitHubAuthorizeUrlResponseObject, error) {
	orgID, aerr, err := s.resolveGitHubInstallationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if s.githubAppClientID == "" {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "github app is not configured on this deployment"), nil
	}
	if s.secretKey == "" {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](http.StatusBadRequest, "encryption_unavailable", "cannot sign the connect flow without an encryption key — set SPACEFLEET_SECRET_KEY"), nil
	}
	if req.Params.InstallationId <= 0 {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](http.StatusBadRequest, "bad_request", "installation_id must be a positive GitHub installation id"), nil
	}
	connect, err := githubapp.VerifyState(s.secretKey, req.Params.State)
	if err != nil || connect.InstallationID != 0 {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](http.StatusBadRequest, "bad_request", "invalid or expired connect state; start the GitHub connection again"), nil
	}
	if connect.Org != orgID {
		return errResp[GetGitHubAuthorizeUrldefaultJSONResponse](http.StatusForbidden, "forbidden", "connect state was issued for a different organization"), nil
	}
	state, err := githubapp.SignInstallationState(s.secretKey, orgID, req.Params.InstallationId)
	if err != nil {
		return nil, err
	}
	// The callback route is the App's registered redirect URI. Without an
	// external URL (route tests) GitHub falls back to the first one registered.
	redirect := ""
	if s.externalURL != "" {
		redirect = strings.TrimRight(s.externalURL, "/") + "/github/callback"
	}
	return GetGitHubAuthorizeUrl200JSONResponse{Url: githubapp.AuthorizeURL(s.githubAppClientID, redirect, state)}, nil
}

// CreateGitHubInstallation records an installation from the connect callback.
// Two checks gate the attach, and both are required: the state token
// (signature, expiry, and that it was issued for the current org) proves the
// caller's org initiated the connect, and the OAuth code exchange inside Link
// proves the GitHub user completing it can access the claimed installation —
// the state alone can't bind the installation id (it is minted before the
// installation exists), and an existence check via the App JWT would accept
// any installation of the App.
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
	state, err := githubapp.VerifyState(s.secretKey, req.Body.State)
	if err != nil {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "invalid or expired connect state; start the GitHub connection again"), nil
	}
	if state.Org != orgID {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusForbidden, "forbidden", "connect state was issued for a different organization"), nil
	}
	// The installation comes from the state on the setup-URL flow (it was
	// bound by authorize-url), or from the callback's query on an App that
	// requests user authorization during installation. Both may be present;
	// they must then agree, so a code can't be redirected at another
	// installation than the one the state was minted for.
	installationID := state.InstallationID
	if req.Body.InstallationId != nil {
		if installationID != 0 && *req.Body.InstallationId != installationID {
			return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "the installation does not match the connect state; start the GitHub connection again"), nil
		}
		installationID = *req.Body.InstallationId
	}
	if installationID <= 0 {
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "missing installation id from GitHub; start the GitHub connection again"), nil
	}
	if req.Body.Code == "" {
		// No code means GitHub didn't run the user-authorization step, so the
		// installation's ownership can't be verified.
		return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "missing authorization code from GitHub; start the GitHub connection again"), nil
	}
	inst, err := s.githubInstallations.Link(ctx, orgID, installationID, req.Body.Code)
	if err != nil {
		if errors.Is(err, githubinstallations.ErrAppNotConfigured) {
			return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "github app is not configured on this deployment"), nil
		}
		// The installation isn't among the ones the authorizing user can access:
		// the ownership check failed, so refuse the attach.
		if errors.Is(err, githubapp.ErrInstallationNotAccessible) {
			return errResp[CreateGitHubInstallationdefaultJSONResponse](http.StatusForbidden, "forbidden", "the GitHub user who completed the install does not have access to this installation"), nil
		}
		// A failure to confirm the installation against GitHub (expired code, App
		// lacks access, GitHub unreachable) is a bad-gateway-ish client-correctable
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
