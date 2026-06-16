//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/membership"
)

// TestGetApplicationComponentOutputs exercises the editor-gated, keys-only
// component-outputs endpoint end to end: an editor gets the latest run's keys
// (no values), a viewer is forbidden, and an app outside the caller's org is a
// 404.
func TestGetApplicationComponentOutputs(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	editorTok, orgID := h.member("editor", membership.RoleEditor)

	// A viewer in the SAME org (so it clears membership and hits the editor gate).
	viewer, err := h.client.User.Create().SetOidcSubject("viewer").SetEmail("viewer@test.local").Save(ctx)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := h.client.Membership.Create().
		SetOrganizationID(orgID).SetUserID(viewer.ID).SetRole(membership.RoleViewer).Save(ctx); err != nil {
		t.Fatalf("add viewer membership: %v", err)
	}

	// An app with one component that captured outputs on a succeeded run.
	runner, err := h.client.Cluster.Create().
		SetOrganizationID(orgID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	app, err := h.client.Application.Create().
		SetOrganizationID(orgID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	infra, err := h.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetName("infra").SetType("terraform").Save(ctx)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	wr, err := h.client.WorkflowRun.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").Save(ctx)
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	if _, err := h.client.ComponentRun.Create().
		SetOrganizationID(orgID).SetWorkflowRunID(wr.ID).SetComponentID(infra.ID).
		SetStatus(componentrun.StatusSucceeded).
		SetOutputs(`{"vpc_id":{"value":"vpc-1","type":"string","sensitive":false},"db_password":{"value":"s","type":"string","sensitive":true}}`).
		SetFinishedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create component run: %v", err)
	}

	path := "/api/applications/" + app.ID.String() + "/component-outputs"

	// Editor: 200 with keys only (and no value field anywhere in the body).
	rec := testReq{method: http.MethodGet, path: path, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("editor got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	var body map[string][]ComponentOutputKey
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	keys := body[infra.ID.String()]
	if len(keys) != 2 {
		t.Fatalf("infra keys = %v, want 2", keys)
	}
	// Sorted: db_password (sensitive), vpc_id.
	if keys[0].Key != "db_password" || !keys[0].Sensitive {
		t.Errorf("keys[0] = %+v, want sensitive db_password", keys[0])
	}
	if keys[1].Key != "vpc_id" || keys[1].Sensitive {
		t.Errorf("keys[1] = %+v, want non-sensitive vpc_id", keys[1])
	}
	// The raw body must never carry a value (the keys-only guarantee).
	if raw := rec.Body.String(); contains(raw, `"value"`) || contains(raw, "vpc-1") || contains(raw, `"s"`) {
		t.Errorf("body leaked an output value: %s", raw)
	}

	// Viewer (below editor): 403.
	rec = testReq{method: http.MethodGet, path: path, token: "viewer", orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer got %d, want 403\n%s", rec.Code, rec.Body.String())
	}

	// An app that isn't in the caller's org: 404.
	missing := "/api/applications/" + uuid.NewString() + "/component-outputs"
	rec = testReq{method: http.MethodGet, path: missing, token: editorTok, orgID: orgID.String()}.do(t, h.handler)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing app got %d, want 404\n%s", rec.Code, rec.Body.String())
	}
}

// contains is a tiny substring check kept local to avoid importing strings just
// for the leak assertion.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
