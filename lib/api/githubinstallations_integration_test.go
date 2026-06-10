//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
)

// createBody builds the connect-callback JSON body, with the OAuth
// authorization code the fake authenticator accepts.
func createBody(installationID int64, state string) string {
	b, _ := json.Marshal(GitHubInstallationCreateRequest{InstallationId: installationID, State: state, Code: "oauth-code"})
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

	b, _ := json.Marshal(GitHubInstallationCreateRequest{InstallationId: 12345, State: h.signState(orgID)})
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
