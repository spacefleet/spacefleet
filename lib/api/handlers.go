package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/applications"
	"github.com/spacefleet/spacefleet/lib/auth"
	"github.com/spacefleet/spacefleet/lib/chartcredentials"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/githubinstallations"
	"github.com/spacefleet/spacefleet/lib/invitations"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/queue"
	"github.com/spacefleet/spacefleet/lib/users"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

type Server struct {
	users               *users.Service
	orgs                *organizations.Service
	clusters            *clusters.Service
	applications        *applications.Service
	chartCredentials    *chartcredentials.Service
	cloudCredentials    *cloudcredentials.Service
	githubInstallations *githubinstallations.Service
	invites             *invitations.Service
	workflows           *workflows.Service

	// githubAppSlug is the operator's GitHub App URL slug, used to build the
	// install link returned by GetGitHubConnectUrl. secretKey signs the
	// short-lived state token that binds that connect flow to the org (the same
	// base64 key as the credential sealer). Both empty when no App is configured.
	githubAppSlug string
	secretKey     string

	// allowOrgCreation gates the create-organization endpoint. When false,
	// the server refuses to mint new organizations (see config.AllowOrgCreation)
	// so only invited users can onboard.
	allowOrgCreation bool

	// externalURL is the canonical public base URL used to build invite links
	// (config.ExternalURL). Required in production; empty only in route tests.
	externalURL string

	// emailEnabled reflects whether SMTP is configured (config.EmailEnabled).
	// When true, CreateInvitation also enqueues an invitation email; either way
	// the response carries a copy-able link. Surfaced to the browser via
	// /config.js so the UI can tune its wording.
	emailEnabled bool

	// jobQueue enqueues background jobs (invitation emails, Tekton installs).
	// Nil-able: handlers degrade gracefully (invites still return a link; a
	// Tekton install that genuinely needs the worker returns 503).
	jobQueue *queue.Client
}

// ServerDeps bundles the runtime dependencies of the API server. Every field is
// optional: a handler whose dependency is missing returns a clear "not
// configured" error instead of panicking, which keeps route-level tests usable
// without a database or queue.
type ServerDeps struct {
	Users               *users.Service
	Orgs                *organizations.Service
	Clusters            *clusters.Service
	Applications        *applications.Service
	ChartCredentials    *chartcredentials.Service
	CloudCredentials    *cloudcredentials.Service
	GitHubInstallations *githubinstallations.Service
	Invites             *invitations.Service
	Workflows           *workflows.Service
	AllowOrgCreation    bool
	ExternalURL         string
	EmailEnabled        bool
	GitHubAppSlug       string
	SecretKey           string
	JobQueue            *queue.Client
}

// NewServer builds the API server from its dependencies.
func NewServer(d ServerDeps) *Server {
	return &Server{
		users:               d.Users,
		orgs:                d.Orgs,
		clusters:            d.Clusters,
		applications:        d.Applications,
		chartCredentials:    d.ChartCredentials,
		cloudCredentials:    d.CloudCredentials,
		githubInstallations: d.GitHubInstallations,
		invites:             d.Invites,
		workflows:           d.Workflows,
		allowOrgCreation:    d.AllowOrgCreation,
		externalURL:         d.ExternalURL,
		emailEnabled:        d.EmailEnabled,
		githubAppSlug:       d.GitHubAppSlug,
		secretKey:           d.SecretKey,
		jobQueue:            d.JobQueue,
	}
}

// enqueueInviteEmail best-effort enqueues an invitation email. It reports
// whether the email was accepted for delivery (enqueued); callers always return
// the invite link regardless, so a false result just means "send it manually".
func (s *Server) enqueueInviteEmail(ctx context.Context, args email.InviteEmailArgs) bool {
	if !s.emailEnabled || s.jobQueue == nil {
		return false
	}
	if _, err := s.jobQueue.Insert(ctx, args); err != nil {
		return false
	}
	return true
}

var _ StrictServerInterface = (*Server)(nil)

// errNoSession indicates the request reached a handler without an authenticated
// session. RequireAuth should make this impossible, so it's a defensive 401.
var errNoSession = errors.New("api: no session on request")

// errNoOrg indicates an org-scoped request arrived without a usable
// X-Organization-ID. Handlers map it to 400.
var errNoOrg = errors.New("api: no organization selected")

func (s *Server) GetHealth(_ context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	return GetHealth200JSONResponse{Status: Ok}, nil
}

// GetMe returns the authenticated user (provisioning them on first sight) and
// the organizations they belong to. The SPA bootstraps from this.
func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[GetMedefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[GetMedefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	mships, err := s.orgs.ListForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	orgs := make([]OrgMembership, 0, len(mships))
	for _, m := range mships {
		if m.Edges.Organization == nil {
			continue
		}
		orgs = append(orgs, OrgMembership{
			Organization: toAPIOrganization(m.Edges.Organization),
			Role:         Role(m.Role),
		})
	}
	return GetMe200JSONResponse{
		User:          User{Id: u.ID, Email: u.Email},
		Organizations: orgs,
	}, nil
}

// CreateOrganization creates an organization owned by the current user.
func (s *Server) CreateOrganization(ctx context.Context, req CreateOrganizationRequestObject) (CreateOrganizationResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[CreateOrganizationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	if !s.allowOrgCreation {
		return errResp[CreateOrganizationdefaultJSONResponse](http.StatusForbidden, "org_creation_disabled", "creating organizations is disabled on this server"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[CreateOrganizationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" {
		return errResp[CreateOrganizationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	org, err := s.orgs.Create(ctx, u.ID, name)
	if err != nil {
		return nil, err
	}
	return CreateOrganization201JSONResponse(toAPIOrganization(org)), nil
}

// RenameOrganization renames an organization the current user owns.
func (s *Server) RenameOrganization(ctx context.Context, req RenameOrganizationRequestObject) (RenameOrganizationResponseObject, error) {
	if s.users == nil || s.orgs == nil {
		return errResp[RenameOrganizationdefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "account service not configured"), nil
	}
	u, err := s.currentUser(ctx)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusUnauthorized, "unauthorized", "no session"), nil
		}
		return nil, err
	}
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" {
		return errResp[RenameOrganizationdefaultJSONResponse](http.StatusBadRequest, "bad_request", "name is required"), nil
	}
	org, err := s.orgs.Rename(ctx, u.ID, req.Id, name)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusNotFound, "not_found", "organization not found"), nil
		case errors.Is(err, organizations.ErrForbidden):
			return errResp[RenameOrganizationdefaultJSONResponse](http.StatusForbidden, "forbidden", "only owners can rename an organization"), nil
		default:
			return nil, err
		}
	}
	return RenameOrganization200JSONResponse(toAPIOrganization(org)), nil
}

// currentUser resolves the authenticated session to a local user record,
// provisioning it on first sight.
func (s *Server) currentUser(ctx context.Context) (*ent.User, error) {
	sess, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errNoSession
	}
	return s.users.EnsureUser(ctx, sess.UserID, sess.Email)
}

// currentOrg resolves the organization the request targets (from the
// X-Organization-ID header, lifted onto the context by auth.OrgContext) and
// verifies the user belongs to it. It returns the caller's membership — from
// which the org id and role are read — or errNoOrg when no/invalid org was
// supplied, or ent's NotFoundError when the user isn't a member.
func (s *Server) currentOrg(ctx context.Context, userID uuid.UUID) (*ent.Membership, error) {
	raw, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, errNoOrg
	}
	orgID, err := uuid.Parse(raw)
	if err != nil {
		return nil, errNoOrg
	}
	return s.orgs.Membership(ctx, userID, orgID)
}

func toAPIOrganization(o *ent.Organization) Organization {
	return Organization{
		Id:        o.ID,
		Name:      o.Name,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

// errResp is a small generic helper so each handler can return its specific
// typed default response without repeating the struct literal. T is bound
// to the per-operation `*defaultJSONResponse` struct.
type defaultResp interface {
	~struct {
		Body       Error
		StatusCode int
	}
}

func errResp[T defaultResp](status int, code, msg string) T {
	return T{Body: Error{Code: code, Message: msg}, StatusCode: status}
}
