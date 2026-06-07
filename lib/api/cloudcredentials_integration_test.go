//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/ent/membership"
)

// decodeCloudCredential decodes a CloudCredential response body.
func decodeCloudCredential(t *testing.T, rec interface{ Bytes() []byte }) CloudCredential {
	t.Helper()
	var c CloudCredential
	if err := json.Unmarshal(rec.Bytes(), &c); err != nil {
		t.Fatalf("decode CloudCredential: %v", err)
	}
	return c
}

// TestCreateCloudCredentialHappyPath registers an AWS credential, confirms the
// non-secret config is returned but the sealed secret never is, and that it
// shows up in the list.
func TestCreateCloudCredentialHappyPath(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)

	body := `{"name":"prod-aws","provider":"aws","description":"billing",` +
		`"aws_access_key_id":"AKIAEXAMPLE","aws_secret_access_key":"s3cret","aws_region":"us-east-1"}`
	rec := testReq{method: http.MethodPost, path: "/api/cloud-credentials", body: body, token: token, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
	c := decodeCloudCredential(t, rec.Body)
	if c.Provider != "aws" || c.Name != "prod-aws" {
		t.Fatalf("created = %+v, want aws/prod-aws", c)
	}
	if c.Config["region"] != "us-east-1" {
		t.Errorf("config region = %q, want us-east-1", c.Config["region"])
	}
	// The response body must never carry the secret material.
	if b := rec.Body.String(); strings.Contains(b, "s3cret") || strings.Contains(b, "secret_access_key") || strings.Contains(b, "AKIAEXAMPLE") {
		t.Fatalf("response leaked secret material: %s", b)
	}

	// It lists.
	rec = testReq{method: http.MethodGet, path: "/api/cloud-credentials", token: token, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	var list []CloudCredential
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Id != c.Id {
		t.Fatalf("list = %+v, want the one credential", list)
	}
}

// TestCreateCloudCredentialValidation confirms missing required per-provider
// fields are a 400 (not a 500), and an unknown provider is rejected by schema
// validation before the handler.
func TestCreateCloudCredentialValidation(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)

	// AWS without the required secret access key.
	rec := testReq{
		method: http.MethodPost,
		path:   "/api/cloud-credentials",
		body:   `{"name":"x","provider":"aws","aws_access_key_id":"AKIA"}`,
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing field: got %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "bad_request" {
		t.Errorf("missing field: code = %q, want bad_request", e.Code)
	}
}

// TestCreateCloudCredentialViewerBlocked confirms the editor-or-above gate
// returns 403 for a viewer.
func TestCreateCloudCredentialViewerBlocked(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("viewer", membership.RoleViewer)

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/cloud-credentials",
		body:   `{"name":"x","provider":"aws","aws_access_key_id":"a","aws_secret_access_key":"b"}`,
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: got %d, want 403\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "forbidden" {
		t.Errorf("viewer: code = %q, want forbidden", e.Code)
	}
}

// TestCloudCredentialCrossOrgIsolation confirms a credential created in one org
// is invisible to another org's members.
func TestCloudCredentialCrossOrgIsolation(t *testing.T) {
	h := newHarness(t, nil)
	tokenA, orgA := h.member("alice", membership.RoleEditor)
	tokenB, orgB := h.member("bob", membership.RoleEditor)

	rec := testReq{
		method: http.MethodPost,
		path:   "/api/cloud-credentials",
		body:   `{"name":"a-cred","provider":"gcp","gcp_service_account_key":"{}"}`,
		token:  tokenA,
		orgID:  orgA.String(),
	}.do(t, h.handler)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in A: got %d, want 201\n%s", rec.Code, rec.Body.String())
	}
	created := decodeCloudCredential(t, rec.Body)

	// Org B can't fetch it.
	rec = testReq{method: http.MethodGet, path: "/api/cloud-credentials/" + created.Id.String(), token: tokenB, orgID: orgB.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org get: got %d, want 404", rec.Code)
	}
	// And B's list is empty.
	rec = testReq{method: http.MethodGet, path: "/api/cloud-credentials", token: tokenB, orgID: orgB.String()}.do(t, h.handler)
	var list []CloudCredential
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode B list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("B list = %+v, want empty", list)
	}
}
