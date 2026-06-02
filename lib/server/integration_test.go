//go:build integration

package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacefleet/spacefleet/lib/api"
	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/users"
)

// TestOrganizationsIntegration drives the real account handlers over HTTP
// against an isolated Postgres database. It uses testsupport.FakeVerifier,
// where the bearer token becomes the user's OIDC subject — so a distinct
// Authorization header is a distinct user. This lets one test cover creation,
// the admin role, and cross-user isolation through the full stack.
func TestOrganizationsIntegration(t *testing.T) {
	client := testsupport.NewEntClient(t)
	h := buildHandler(
		&config.Config{Addr: ":0", Env: "test", AllowOrgCreation: true},
		api.ServerDeps{
			Users:            users.NewService(client),
			Orgs:             organizations.NewService(client),
			AllowOrgCreation: true,
		},
		testsupport.FakeVerifier(),
	)

	// Alice starts with no organizations.
	var me struct {
		User struct {
			Id    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		Organizations []struct {
			Organization struct {
				Id   string `json:"id"`
				Name string `json:"name"`
			} `json:"organization"`
			Role string `json:"role"`
		} `json:"organizations"`
	}
	rec := do(t, h, http.MethodGet, "/api/me", "alice", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me (alice, empty): got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	mustDecode(t, rec, &me)
	if len(me.Organizations) != 0 {
		t.Fatalf("alice should start with no orgs, got %+v", me.Organizations)
	}

	// Alice creates an organization and becomes its admin.
	createBody, _ := json.Marshal(map[string]string{"name": "Acme Inc."})
	rec = do(t, h, http.MethodPost, "/api/organizations", "alice", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var created struct {
		Id   string `json:"id"`
		Name string `json:"name"`
	}
	mustDecode(t, rec, &created)
	if created.Id == "" || created.Name != "Acme Inc." {
		t.Fatalf("create: unexpected org %+v", created)
	}

	// /api/me now reflects the membership, with the admin role.
	rec = do(t, h, http.MethodGet, "/api/me", "alice", nil)
	mustDecode(t, rec, &me)
	if len(me.Organizations) != 1 {
		t.Fatalf("alice should have exactly one org, got %+v", me.Organizations)
	}
	if got := me.Organizations[0]; got.Organization.Id != created.Id || got.Role != "admin" {
		t.Fatalf("membership: got %+v, want org %s as admin", got, created.Id)
	}

	// Bob is a different user and must not see Alice's organization.
	rec = do(t, h, http.MethodGet, "/api/me", "bob", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me (bob): got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	mustDecode(t, rec, &me)
	if len(me.Organizations) != 0 {
		t.Fatalf("bob should not see alice's org, got %+v", me.Organizations)
	}

	// Bob can't rename Alice's org — he's not a member (404).
	renameBody, _ := json.Marshal(map[string]string{"name": "Hacked"})
	rec = do(t, h, http.MethodPatch, "/api/organizations/"+created.Id, "bob", renameBody)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob rename: got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}

	// Alice (the admin) can rename it.
	rec = do(t, h, http.MethodPatch, "/api/organizations/"+created.Id, "alice", renameBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("alice rename: got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
}

// TestClustersIntegration drives the cluster handlers over HTTP, exercising the
// org-scoping seam (X-Organization-ID) end to end: a member can register/list
// clusters in their org, a non-member is forbidden, and a missing org header is
// a 400.
func TestClustersIntegration(t *testing.T) {
	client := testsupport.NewEntClient(t)
	key := make([]byte, 32)
	sealer, err := secrets.NewSealer(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	h := buildHandler(
		&config.Config{Addr: ":0", Env: "test", AllowOrgCreation: true},
		api.ServerDeps{
			Users:            users.NewService(client),
			Orgs:             organizations.NewService(client),
			Clusters:         clusters.NewService(client, sealer),
			AllowOrgCreation: true,
		},
		testsupport.FakeVerifier(),
	)

	// Alice creates an org and learns its id.
	createBody, _ := json.Marshal(map[string]string{"name": "Acme Inc."})
	rec := do(t, h, http.MethodPost, "/api/organizations", "alice", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org: got %d (body %s)", rec.Code, rec.Body)
	}
	var org struct {
		Id string `json:"id"`
	}
	mustDecode(t, rec, &org)

	// Registering a cluster with no org header is a 400.
	clusterBody, _ := json.Marshal(map[string]string{"name": "host", "connection_method": "in_cluster"})
	rec = do(t, h, http.MethodPost, "/api/clusters", "alice", clusterBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create cluster without org: got %d, want 400 (body %s)", rec.Code, rec.Body)
	}

	// With the org header, Alice registers an in-cluster cluster. The probe
	// fails (tests don't run in k8s), so it lands in the error status — but the
	// row is created and returned.
	rec = doOrg(t, h, http.MethodPost, "/api/clusters", "alice", org.Id, clusterBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create cluster: got %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	var c struct {
		Id               string `json:"id"`
		ConnectionMethod string `json:"connection_method"`
		Status           string `json:"status"`
	}
	mustDecode(t, rec, &c)
	if c.ConnectionMethod != "in_cluster" || c.Status != "error" {
		t.Fatalf("cluster: got method=%q status=%q, want in_cluster/error", c.ConnectionMethod, c.Status)
	}

	// Alice lists her org's clusters.
	rec = doOrg(t, h, http.MethodGet, "/api/clusters", "alice", org.Id, nil)
	var list []struct {
		Id string `json:"id"`
	}
	mustDecode(t, rec, &list)
	if len(list) != 1 || list[0].Id != c.Id {
		t.Fatalf("list: got %+v, want one cluster %s", list, c.Id)
	}

	// Bob is not a member of Alice's org → forbidden.
	rec = doOrg(t, h, http.MethodGet, "/api/clusters", "bob", org.Id, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob list: got %d, want 403 (body %s)", rec.Code, rec.Body)
	}

	// Alice deletes the cluster.
	rec = doOrg(t, h, http.MethodDelete, "/api/clusters/"+c.Id, "alice", org.Id, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204 (body %s)", rec.Code, rec.Body)
	}
}

func do(t *testing.T, h http.Handler, method, path, asUser string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	// The fake verifier uses the bearer token as the user identity.
	if asUser != "" {
		r.Header.Set("Authorization", "Bearer "+asUser)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// doOrg is like do but also sets the X-Organization-ID header, targeting an
// org-scoped endpoint at a specific tenant.
func doOrg(t *testing.T, h http.Handler, method, path, asUser, orgID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if asUser != "" {
		r.Header.Set("Authorization", "Bearer "+asUser)
	}
	if orgID != "" {
		r.Header.Set("X-Organization-ID", orgID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}
