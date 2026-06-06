//go:build integration

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent/membership"
)

// The DB-backed half of the SSE coverage: the pre-stream not-found / 409
// classifications and the snapshot-then-terminal-close happy path. These need
// real application rows, so they're integration-tagged; the nil-service paths
// run tag-free in stream_test.go.

// TestStreamApplicationNotFound confirms a missing application id surfaces as a
// normal HTTP 404 (not a half-open stream): the initial Get doubles as the
// existence check.
func TestStreamApplicationNotFound(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)

	rec := testReq{
		method: http.MethodGet,
		path:   "/api/applications/" + uuid.NewString() + "/stream",
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "not_found" {
		t.Fatalf("error code = %q, want not_found", e.Code)
	}
}

// TestStreamApplicationLogsNoRun confirms streaming logs for an application that
// has never rolled out is a 409 no_run, classified before any cluster reach
// (appForStream gates on an empty LastRunName).
func TestStreamApplicationLogsNoRun(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)
	app := h.terminalApp(orgID, "") // no last run

	rec := testReq{
		method: http.MethodGet,
		path:   "/api/applications/" + app.ID.String() + "/logs",
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "no_run" {
		t.Fatalf("error code = %q, want no_run", e.Code)
	}
}

// TestStreamApplicationLogsStaleRun confirms a ?run= that no longer matches the
// app's current rollout is a 409 stale_run — classified before resolving the
// run's pod (so it never reaches the cluster), telling the client to reconnect
// on the new run.
func TestStreamApplicationLogsStaleRun(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)
	app := h.terminalApp(orgID, "run-current")

	rec := testReq{
		method: http.MethodGet,
		path:   "/api/applications/" + app.ID.String() + "/logs?run=run-stale",
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409\n%s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Code != "stale_run" {
		t.Fatalf("error code = %q, want stale_run", e.Code)
	}
}

// TestStreamApplicationSnapshotThenClose is the happy path: a rollout-terminal
// application yields the initial `status` snapshot event, then the stream closes
// immediately (appTerminal => return) rather than blocking on a poll. We assert
// a 200 event-stream carrying one status event with the app's deployed state.
func TestStreamApplicationSnapshotThenClose(t *testing.T) {
	h := newHarness(t, nil)
	token, orgID := h.member("editor", membership.RoleEditor)
	app := h.terminalApp(orgID, "run-1")

	rec := testReq{
		method: http.MethodGet,
		path:   "/api/applications/" + app.ID.String() + "/stream",
		token:  token,
		orgID:  orgID.String(),
	}.do(t, h.handler)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: status") {
		t.Fatalf("expected a status event, got:\n%s", body)
	}
	// The snapshot data carries the deployed status (it's a terminal app).
	if !strings.Contains(body, `"status":"deployed"`) {
		t.Fatalf("expected deployed status in snapshot, got:\n%s", body)
	}
	// A terminal app closes after one event — no second status frame.
	if n := strings.Count(body, "event: status"); n != 1 {
		t.Fatalf("expected exactly one status event, got %d:\n%s", n, body)
	}
}
