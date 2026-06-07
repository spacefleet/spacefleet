//go:build integration

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/applications"
	"github.com/spacefleet/spacefleet/lib/organizations"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/users"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// This file is the DB-backed half of the SSE stream tests: it drives a real
// workflow run through state changes over an http.Server and asserts the
// StreamApplicationRun handler emits a `snapshot` per change, redacts the
// secret-bearing component `values` for a viewer, and closes the stream when the
// run reaches a terminal status. It is integration-tagged because it needs a
// real Postgres (the tag-free stream_test.go covers the nil-service 503 paths).

// streamFixture wires the real services StreamApplicationRun needs behind the
// shared handler tree, over a freshly-migrated database.
type streamFixture struct {
	t         *testing.T
	client    *ent.Client
	server    *httptest.Server
	workflows *workflows.Service
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	client := testsupport.NewEntClient(t)
	wf := workflows.NewService(client)
	handler := newTestHandler(ServerDeps{
		Users:        users.NewService(client),
		Orgs:         organizations.NewService(client),
		Applications: applications.NewService(client),
		Workflows:    wf,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &streamFixture{t: t, client: client, server: srv, workflows: wf}
}

// member provisions a user (subject == token) and an org with that user at role,
// returning the bearer token and org id. Mirrors the integration handler harness.
func (f *streamFixture) member(token string, role membership.Role) (string, uuid.UUID) {
	f.t.Helper()
	ctx := context.Background()
	u, err := f.client.User.Create().SetOidcSubject(token).SetEmail(token + "@test.local").Save(ctx)
	if err != nil {
		f.t.Fatalf("create user: %v", err)
	}
	org, err := f.client.Organization.Create().SetName("Org-" + token).Save(ctx)
	if err != nil {
		f.t.Fatalf("create org: %v", err)
	}
	if _, err := f.client.Membership.Create().
		SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(role).Save(ctx); err != nil {
		f.t.Fatalf("create membership: %v", err)
	}
	return token, org.ID
}

// seedApp creates an application with one helm component carrying a secret in its
// `values`, returning the app id.
func (f *streamFixture) seedApp(orgID uuid.UUID) uuid.UUID {
	f.t.Helper()
	ctx := context.Background()
	mkCluster := func(name string) uuid.UUID {
		c, err := f.client.Cluster.Create().
			SetOrganizationID(orgID).SetName(name).
			SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
		if err != nil {
			f.t.Fatalf("create cluster: %v", err)
		}
		return c.ID
	}
	app, err := f.client.Application.Create().
		SetOrganizationID(orgID).SetName("web").SetTargetNamespace("apps").
		SetTargetClusterID(mkCluster("target")).SetRunnerClusterID(mkCluster("runner")).Save(ctx)
	if err != nil {
		f.t.Fatalf("create app: %v", err)
	}
	if _, err := f.client.Component.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).
		SetName("api").SetType("helm").
		SetConfig(map[string]string{"chart": "api", "values": "password: hunter2"}).
		Save(ctx); err != nil {
		f.t.Fatalf("create component: %v", err)
	}
	return app.ID
}

// sseEvent is one parsed `event:`/`data:` frame from the stream.
type sseEvent struct {
	name string
	data string
}

// readEvents reads the stream until it closes (the handler returns on terminal),
// returning the parsed events. The caller bounds it with the request context.
func readEvents(t *testing.T, body *bufio.Reader) []sseEvent {
	t.Helper()
	var (
		events []sseEvent
		cur    sseEvent
	)
	for {
		line, err := body.ReadString('\n')
		if line != "" {
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			case line == "\n":
				if cur.name != "" {
					events = append(events, cur)
					cur = sseEvent{}
				}
			}
		}
		if err != nil {
			return events
		}
	}
}

// openStream opens the run stream as token and returns the parsed events once the
// stream closes. ctx bounds the read so a test never hangs.
func (f *streamFixture) openStream(ctx context.Context, token string, orgID, appID, runID uuid.UUID) []sseEvent {
	f.t.Helper()
	url := f.server.URL + "/api/applications/" + appID.String() + "/runs/" + runID.String() + "/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Organization-ID", orgID.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		f.t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	return readEvents(f.t, bufio.NewReader(resp.Body))
}

// TestStreamRunEmitsSnapshotsAndClosesOnTerminal proves an editor's stream emits
// the initial snapshot, a snapshot per state change as the run progresses, and
// then closes when the run settles — with the secret `values` present for an
// editor.
func TestStreamRunEmitsSnapshotsAndClosesOnTerminal(t *testing.T) {
	f := newStreamFixture(t)
	token, orgID := f.member("editor", membership.RoleEditor)
	appID := f.seedApp(orgID)

	run, err := f.workflows.BeginRun(context.Background(), orgID, appID, workflows.ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Drive the run through state changes from a second goroutine while the stream
	// is open: pending -> running -> succeeded. The poller (1s) coalesces these
	// into at least the initial + a later snapshot, then closes on terminal.
	go func() {
		bg := context.Background()
		time.Sleep(250 * time.Millisecond)
		_ = f.workflows.MarkRun(bg, orgID, run.ID, string(workflowrun.StatusRunning), "")
		time.Sleep(1500 * time.Millisecond)
		_ = f.workflows.MarkRun(bg, orgID, run.ID, string(workflowrun.StatusSucceeded), "done")
	}()

	events := f.openStream(ctx, token, orgID, appID, run.ID)
	if ctx.Err() != nil {
		t.Fatalf("stream did not close before deadline (terminal not honored): %v", ctx.Err())
	}
	if len(events) < 2 {
		t.Fatalf("got %d events, want >=2 (initial + a change): %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.name != "snapshot" {
			t.Errorf("event name = %q, want snapshot", ev.name)
		}
	}
	// The last snapshot is the terminal one.
	last := events[len(events)-1]
	var detail WorkflowRunDetail
	if err := json.Unmarshal([]byte(last.data), &detail); err != nil {
		t.Fatalf("decode final snapshot %q: %v", last.data, err)
	}
	if detail.Status != RunStatusSucceeded {
		t.Errorf("final status = %q, want succeeded", detail.Status)
	}
	// An editor sees the secret-bearing values in the graph snapshot.
	if detail.Graph == nil || !strings.Contains(*detail.Graph, "hunter2") {
		t.Errorf("editor snapshot should retain secret values, graph = %v", detail.Graph)
	}
}

// TestStreamRunRedactsSecretsForViewer proves a viewer's stream withholds the
// secret-bearing `values` from the graph snapshot (redacted), while still
// delivering the run status. The run is created already-terminal so the stream
// closes immediately after the single snapshot.
func TestStreamRunRedactsSecretsForViewer(t *testing.T) {
	f := newStreamFixture(t)
	token, orgID := f.member("viewer", membership.RoleViewer)
	appID := f.seedApp(orgID)

	run, err := f.workflows.BeginRun(context.Background(), orgID, appID, workflows.ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	// Settle the run before opening the stream: the handler emits one snapshot and
	// closes (runTerminal short-circuits the poll loop).
	if err := f.workflows.MarkRun(context.Background(), orgID, run.ID, string(workflowrun.StatusSucceeded), "done"); err != nil {
		t.Fatalf("MarkRun: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events := f.openStream(ctx, token, orgID, appID, run.ID)
	if ctx.Err() != nil {
		t.Fatalf("stream did not close before deadline: %v", ctx.Err())
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1 (snapshot of a terminal run): %+v", len(events), events)
	}

	var detail WorkflowRunDetail
	if err := json.Unmarshal([]byte(events[0].data), &detail); err != nil {
		t.Fatalf("decode snapshot %q: %v", events[0].data, err)
	}
	if detail.Status != RunStatusSucceeded {
		t.Errorf("status = %q, want succeeded", detail.Status)
	}
	// The viewer must NOT see the secret values anywhere in the snapshot.
	if detail.Graph != nil && strings.Contains(*detail.Graph, "hunter2") {
		t.Errorf("viewer snapshot leaked secret values: %v", *detail.Graph)
	}
	// Redaction strips only the secret key — the non-secret chart key survives.
	if detail.Graph == nil || !strings.Contains(*detail.Graph, "\"chart\"") {
		t.Errorf("viewer snapshot should retain non-secret config, graph = %v", detail.Graph)
	}
}

// TestStreamRunCrossOrgNotFound proves a run in org A streams as 404 for a member
// of org B — the stream reuses GetRun's org-scoped auth, so it is no side door.
func TestStreamRunCrossOrgNotFound(t *testing.T) {
	f := newStreamFixture(t)
	_, orgA := f.member("alice", membership.RoleEditor)
	tokenB, orgB := f.member("bob", membership.RoleEditor)
	appID := f.seedApp(orgA)

	run, err := f.workflows.BeginRun(context.Background(), orgA, appID, workflows.ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	url := f.server.URL + "/api/applications/" + appID.String() + "/runs/" + run.ID.String() + "/stream"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+tokenB)
	req.Header.Set("X-Organization-ID", orgB.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org stream status = %d, want 404", resp.StatusCode)
	}
}
