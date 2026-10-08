//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/lib/githubapp"
)

// createBody builds the connect-callback JSON body, with the OAuth
// authorization code the fake authenticator accepts. A zero installationID
// leaves it out (the setup-URL flow, where the state carries it).
func createBody(installationID int64, state string) string {
	req := GitHubInstallationCreateRequest{State: state, Code: "oauth-code"}
	if installationID != 0 {
		req.InstallationId = &installationID
	}
	b, _ := json.Marshal(req)
	return string(b)
}

// TestCreateGitHubInstallationViewerBlocked confirms a viewer can't attach an
// installation: the editor-or-above role gate in resolveGitHubInstallationsWrite
// returns 403 before any state work.
func TestCreateGitHubInstallationViewerBlocked(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("viewer", membership.RoleViewer)

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   createBody(12345, h.signState(orgID)),
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "forbidden" {
		t.Fatalf("viewer: error code = %q, want forbidden", e.Code)
	}
}

// TestCreateGitHubInstallationCrossOrgGuard is the core H2 regression: an editor
// of org A presents a state token validly signed for org B. The App JWT could
// read B's installation, so the ONLY thing stopping the attach is the
// stateOrg != orgID guard — it must be 403.
func TestCreateGitHubInstallationCrossOrgGuard(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgA := h.member("editor", membership.RoleEditor)
	orgB := h.newOrgID() // an org the caller does NOT belong to

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		// State validly signed for org B, but the request targets org A.
		body:  createBody(999, h.signState(orgB)),
		token: token,
		orgID: orgA.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org: got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "forbidden" {
		t.Fatalf("cross-org: error code = %q, want forbidden", e.Code)
	}

	// And nothing was linked: the org has no installations.
	if list, err := h.client.GitHubInstallation.Query().All(context.Background()); err != nil {
		t.Fatalf("list installations: %v", err)
	} else if len(list) != 0 {
		t.Fatalf("cross-org attach leaked %d installation(s)", len(list))
	}
}

// TestCreateGitHubInstallationHijackBlocked is the C1 regression: an editor
// with a state validly signed for their OWN org claims an installation id the
// authorizing GitHub user cannot access (e.g. a victim org's installation —
// ids are small sequential integers). The user-ownership check inside Link
// must refuse with 403 and record nothing; before the fix, an App-JWT
// existence check accepted any installation of the App.
func TestCreateGitHubInstallationHijackBlocked(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme", inaccessible: map[int64]bool{31337: true}})
	token, orgID := h.member("editor", membership.RoleEditor)

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   createBody(31337, h.signState(orgID)), // state is for the caller's own org
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("hijack attempt: got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "forbidden" {
		t.Fatalf("hijack attempt: error code = %q, want forbidden", e.Code)
	}
	if list, err := h.client.GitHubInstallation.Query().All(context.Background()); err != nil {
		t.Fatalf("list installations: %v", err)
	} else if len(list) != 0 {
		t.Fatalf("hijack attach leaked %d installation(s)", len(list))
	}
}

// TestCreateGitHubInstallationMissingCode confirms a callback without the OAuth
// authorization code is refused (400) before any GitHub call — without the code
// the ownership of the installation cannot be verified.
func TestCreateGitHubInstallationMissingCode(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)

	id := int64(12345)
	b, _ := json.Marshal(GitHubInstallationCreateRequest{InstallationId: &id, State: h.signState(orgID)})
	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   string(b),
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing code: got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "bad_request" {
		t.Fatalf("missing code: error code = %q, want bad_request", e.Code)
	}
}

// TestCreateGitHubInstallationBadState confirms a garbage/expired state token is
// a 400 (invalid connect state), distinct from the cross-org 403 — VerifyState
// fails before the org comparison.
func TestCreateGitHubInstallationBadState(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)

	for _, state := range []string{"not-a-valid-token", "", "abc.def"} {
		rec := testReq{
			method: http.MethodPost,
			path:   "/api/github/installations",
			body:   createBody(12345, state),
			token:  token,
			orgID:  orgID.String(),
		}.do(t, h.handler)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("garbage state %q: got %d, want 400\n%s", state, rec.Code, rec.Body.String())
		}
		if e := decodeErr(t, rec); e.Code != "bad_request" {
			t.Fatalf("garbage state %q: error code = %q, want bad_request", state, e.Code)
		}
	}
}

// TestCreateGitHubInstallationHappyPath confirms an editor with a state token
// signed for their own org links the installation: 201 with the recorded row,
// and the body carries no secret (there is none to leak).
func TestCreateGitHubInstallationHappyPath(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   createBody(424242, h.signState(orgID)),
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusCreated {
		t.Fatalf("happy path: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
	var got GitHubInstallation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode created installation: %v", err)
	}
	if got.InstallationId != 424242 {
		t.Errorf("installation_id = %d, want 424242", got.InstallationId)
	}
	if got.AccountLogin == nil || *got.AccountLogin != "acme" {
		t.Errorf("account_login = %v, want acme (from the authenticator)", got.AccountLogin)
	}

	// It was actually persisted, scoped to the org.
	if list, err := h.client.GitHubInstallation.Query().All(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(list) != 1 || list[0].OrganizationID != orgID {
		t.Fatalf("expected 1 installation scoped to org %s, got %+v", orgID, list)
	}
}

// TestGetGitHubAuthorizeUrl: the setup-URL flow's authorize step — an editor
// presenting their org's connect state gets GitHub's OAuth authorize URL for
// the App, coming back to this deployment's /github/callback, with a state
// bound to the org and the installation. Another org's state, an
// installation-bound state, a missing or garbage state, a bad installation
// id, and a viewer are all refused.
func TestGetGitHubAuthorizeUrl(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)
	authorizePath := func(id, state string) string {
		return "/api/github/installations/authorize-url?" + url.Values{"installation_id": {id}, "state": {state}}.Encode()
	}

	rec := testReq{
		method: http.MethodGet,
		path:   authorizePath("4242", h.signState(orgID)),
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize-url: got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	var got GitHubConnectUrl
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(got.Url)
	if err != nil {
		t.Fatalf("parse %q: %v", got.Url, err)
	}
	q := u.Query()
	if u.Host != "github.com" || u.Path != "/login/oauth/authorize" {
		t.Errorf("url = %s, want github.com/login/oauth/authorize", got.Url)
	}
	if q.Get("client_id") != "Iv1.test" || q.Get("redirect_uri") != "https://sf.example.com/github/callback" {
		t.Errorf("client_id/redirect_uri = %q/%q", q.Get("client_id"), q.Get("redirect_uri"))
	}
	state, err := githubapp.VerifyState(testSecretKey, q.Get("state"))
	if err != nil || state.Org != orgID || state.InstallationID != 4242 {
		t.Errorf("state = %+v (%v), want org %s installation 4242", state, err, orgID)
	}

	bound, err := githubapp.SignInstallationState(testSecretKey, orgID, 4242)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"zero id", authorizePath("0", h.signState(orgID)), http.StatusBadRequest},
		{"negative id", authorizePath("-1", h.signState(orgID)), http.StatusBadRequest},
		{"garbage state", authorizePath("4242", "abc.def"), http.StatusBadRequest},
		{"no state", "/api/github/installations/authorize-url?installation_id=4242", http.StatusBadRequest},
		{"installation-bound state", authorizePath("4242", bound), http.StatusBadRequest},
		{"another org's state", authorizePath("4242", h.signState(h.newOrgID())), http.StatusForbidden},
	} {
		rec := testReq{method: http.MethodGet, path: tc.path, token: token, orgID: orgID.String()}.do(t, h.handler)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d\n%s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	viewer, viewerOrg := h.member("viewer", membership.RoleViewer)
	rec = testReq{
		method: http.MethodGet,
		path:   authorizePath("4242", h.signState(viewerOrg)),
		token:  viewer,
		orgID:  viewerOrg.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: got %d, want 403", rec.Code)
	}
}

// TestCreateGitHubInstallationFromAuthorizeState: on the setup-URL flow the
// callback carries only the code and the authorize state, which names the
// installation. It links that installation; a body id that disagrees with the
// state is refused, and a state that names no installation needs a body id.
func TestCreateGitHubInstallationFromAuthorizeState(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)
	bound, err := githubapp.SignInstallationState(testSecretKey, orgID, 4242)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		return testReq{method: http.MethodPost, path: "/api/github/installations", body: body, token: token, orgID: orgID.String()}.do(t, h.handler)
	}

	if rec := post(createBody(999, bound)); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched installation: got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	if rec := post(createBody(0, h.signState(orgID))); rec.Code != http.StatusBadRequest {
		t.Fatalf("no installation anywhere: got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	if n, _ := h.client.GitHubInstallation.Query().Count(context.Background()); n != 0 {
		t.Fatalf("refused requests linked %d installation(s)", n)
	}

	rec := post(createBody(0, bound))
	if rec.Code != http.StatusCreated {
		t.Fatalf("state-bound create: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
	var got GitHubInstallation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.InstallationId != 4242 {
		t.Errorf("installation_id = %d, want 4242 (from the state)", got.InstallationId)
	}
	// The same id in the body as in the state is fine (and idempotent).
	if rec := post(createBody(4242, bound)); rec.Code != http.StatusCreated {
		t.Fatalf("matching body id: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
}

// TestListGitHubRepositories confirms the picker endpoint returns the
// repositories the org's installations can reach, each tagged with the
// installation record id (so the UI can select it) and the account login.
func TestListGitHubRepositories(t *testing.T) {
	h := newHarness(t, fakeGitHubAuth{login: "acme"})
	token, orgID := h.member("editor", membership.RoleEditor)

	// Link an installation so the org has one to list repositories from.
	rec := testReq{
		method: http.MethodPost,
		path:   "/api/github/installations",
		body:   createBody(424242, h.signState(orgID)),
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusCreated {
		t.Fatalf("link installation: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
	var inst GitHubInstallation
	if err := json.Unmarshal(rec.Body.Bytes(), &inst); err != nil {
		t.Fatalf("decode installation: %v", err)
	}

	rec = testReq{
		method: http.MethodGet,
		path:   "/api/github/repositories",
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("list repositories: got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	var repos []GitHubRepository
	if err := json.Unmarshal(rec.Body.Bytes(), &repos); err != nil {
		t.Fatalf("decode repositories: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}
	if repos[0].FullName != "acme/charts" {
		t.Errorf("full_name = %q, want acme/charts", repos[0].FullName)
	}
	if repos[0].InstallationId != inst.Id {
		t.Errorf("installation_id = %v, want %v (the record id)", repos[0].InstallationId, inst.Id)
	}
	if repos[0].AccountLogin == nil || *repos[0].AccountLogin != "acme" {
		t.Errorf("account_login = %v, want acme", repos[0].AccountLogin)
	}
}
