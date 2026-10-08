//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/membership"
	"github.com/spacefleet/spacefleet/ent/tofustateversion"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/tofustate"
)

// stateFixture is an application with one running workflow run and the
// steps (component runs) state tokens are minted for.
type stateFixture struct {
	h         *harness
	signer    *tofustate.Signer
	orgID     uuid.UUID
	appID     uuid.UUID
	component uuid.UUID
	runID     uuid.UUID
}

func newStateFixture(t *testing.T) *stateFixture {
	t.Helper()
	h := newHarness(t, nil)
	ctx := context.Background()
	_, orgID := h.member("editor", membership.RoleEditor)
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
	run, err := h.client.WorkflowRun.Create().
		SetOrganizationID(orgID).SetApplicationID(app.ID).SetAction("deploy").
		SetStatus(workflowrun.StatusRunning).SetStartedBy("alice@example.com").Save(ctx)
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	signer, err := tofustate.NewSigner(testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	return &stateFixture{h: h, signer: signer, orgID: orgID, appID: app.ID, component: uuid.New(), runID: run.ID}
}

// step creates a running component run (an execution step) of the run.
func (f *stateFixture) step(t *testing.T) uuid.UUID {
	t.Helper()
	cr, err := f.h.client.ComponentRun.Create().
		SetOrganizationID(f.orgID).SetWorkflowRunID(f.runID).SetComponentID(f.component).
		SetStatus(componentrun.StatusRunning).Save(context.Background())
	if err != nil {
		t.Fatalf("create component run: %v", err)
	}
	return cr.ID
}

// token mints a state token for a step of the fixture's component.
func (f *stateFixture) token(t *testing.T, step uuid.UUID, scope string) string {
	t.Helper()
	tok, err := f.signer.Sign(tofustate.Claims{
		OrgID: f.orgID, ApplicationID: f.appID, ComponentID: f.component, Workspace: "default",
		WorkflowRunID: f.runID, ComponentRunID: step, Scope: scope,
		Expiry: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *stateFixture) path() string {
	return "/api/tofu/state/" + f.component.String() + "/default"
}

// stateReq issues a backend request the way OpenTofu's `http` backend does:
// basic auth with the token as password, Content-MD5 on a body.
func stateReq(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.SetBasicAuth(tofustate.Username, token)
	}
	if body != nil {
		sum := md5.Sum(body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func lockBody(id string) []byte {
	b, _ := json.Marshal(tofustate.LockInfo{ID: id, Operation: "OperationTypeApply", Who: "runner@pod", Version: "1.10.0"})
	return b
}

func stateBody(lineage string, serial int, marker string) []byte {
	return []byte(fmt.Sprintf(`{"version":4,"terraform_version":"1.10.0","serial":%d,"lineage":%q,"outputs":{"m":{"value":%q,"type":"string"}},"resources":[]}`, serial, lineage, marker))
}

// TestTofuStateProtocol drives the managed state endpoints through a whole
// plan → apply cycle the way OpenTofu's http backend does, plus every
// refusal: auth, scope, the lock, and the write guards.
func TestTofuStateProtocol(t *testing.T) {
	f := newStateFixture(t)
	h := f.h.handler
	plan := f.step(t)
	apply := f.step(t)
	planTok := f.token(t, plan, tofustate.ScopeRead)
	applyTok := f.token(t, apply, tofustate.ScopeWrite)
	path := f.path()

	// Auth: none, garbage, and a valid token for another component's path.
	if rec := stateReq(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d, want 401", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodGet, path, "nope.nope", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage token: %d, want 401", rec.Code)
	}
	other := "/api/tofu/state/" + uuid.NewString() + "/default"
	if rec := stateReq(t, h, http.MethodGet, other, planTok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("other component: %d, want 403", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodGet, "/api/tofu/state/"+f.component.String()+"/prod", planTok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("other workspace: %d, want 403", rec.Code)
	}

	// Nothing written yet.
	if rec := stateReq(t, h, http.MethodGet, path, planTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("empty state: %d, want 204", rec.Code)
	}

	// The plan step locks (twice — idempotent), the apply step is refused
	// with the holder's info, and a read token can't write.
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", planTok, lockBody("plan-lock")); rec.Code != http.StatusOK {
		t.Fatalf("plan lock: %d %s", rec.Code, rec.Body)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", planTok, lockBody("plan-lock")); rec.Code != http.StatusOK {
		t.Fatalf("plan re-lock: %d %s", rec.Code, rec.Body)
	}
	rec := stateReq(t, h, http.MethodPost, path+"/lock", applyTok, lockBody("apply-lock"))
	if rec.Code != http.StatusLocked {
		t.Fatalf("contended lock: %d, want 423", rec.Code)
	}
	var holder tofustate.LockInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &holder); err != nil || holder.ID != "plan-lock" || holder.Who != "runner@pod" {
		t.Fatalf("423 body = %s, want the plan step's lock info", rec.Body)
	}
	// A lock id learned from the 423 can't take the lock over.
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", applyTok, lockBody("plan-lock")); rec.Code != http.StatusLocked {
		t.Fatalf("lock takeover by id: %d, want 423", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=plan-lock", planTok, stateBody("lin-1", 1, "a")); rec.Code != http.StatusForbidden {
		t.Fatalf("read-token write: %d, want 403", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodDelete, path+"/lock", planTok, lockBody("plan-lock")); rec.Code != http.StatusOK {
		t.Fatalf("plan unlock: %d %s", rec.Code, rec.Body)
	}

	// The apply step: lock, then writes.
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", applyTok, lockBody("apply-lock")); rec.Code != http.StatusOK {
		t.Fatalf("apply lock: %d %s", rec.Code, rec.Body)
	}
	v1 := stateBody("lin-1", 1, "a")
	if rec := stateReq(t, h, http.MethodPost, path, applyTok, v1); rec.Code != http.StatusConflict {
		t.Fatalf("write without lock id: %d, want 409", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=wrong", applyTok, v1); rec.Code != http.StatusConflict {
		t.Fatalf("write with another lock id: %d, want 409", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, path+"?ID=apply-lock", bytes.NewReader(v1))
	req.SetBasicAuth(tofustate.Username, applyTok)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString([]byte("not the md5 of it")))
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("md5 mismatch: %d, want 400", bad.Code)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=apply-lock", applyTok, []byte(`{"serial":1}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("state without lineage: %d, want 400", rec.Code)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=apply-lock", applyTok, v1); rec.Code != http.StatusOK {
		t.Fatalf("first write: %d %s", rec.Code, rec.Body)
	}
	// A byte-identical retry is a no-op; the same serial with other bytes,
	// an older serial, and another lineage are refused.
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=apply-lock", applyTok, v1); rec.Code != http.StatusOK {
		t.Fatalf("identical retry: %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string][]byte{
		"same serial, other bytes": stateBody("lin-1", 1, "b"),
		"older serial":             stateBody("lin-1", 0, "b"),
		"other lineage":            stateBody("lin-2", 2, "b"),
	} {
		if rec := stateReq(t, h, http.MethodPost, path+"?ID=apply-lock", applyTok, body); rec.Code != http.StatusConflict {
			t.Errorf("%s: %d, want 409", name, rec.Code)
		}
	}
	v2 := stateBody("lin-1", 2, "c")
	if rec := stateReq(t, h, http.MethodPost, path+"?ID=apply-lock", applyTok, v2); rec.Code != http.StatusOK {
		t.Fatalf("second write: %d %s", rec.Code, rec.Body)
	}

	// The read returns the latest version with its MD5.
	rec = stateReq(t, h, http.MethodGet, path, planTok, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), v2) {
		t.Fatalf("read: %d %s, want the second write", rec.Code, rec.Body)
	}
	sum := md5.Sum(v2)
	if got := rec.Header().Get("Content-MD5"); got != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("Content-MD5 = %q", got)
	}

	// Two versions, sealed (the plaintext isn't in the column), credited to
	// the run's starter and the apply step.
	versions, err := f.h.client.TofuStateVersion.Query().Order(ent.Asc(tofustateversion.FieldVersion)).All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 1 || versions[1].Version != 2 || versions[1].Serial != 2 {
		t.Fatalf("versions = %+v", versions)
	}
	for _, v := range versions {
		if bytes.Contains(v.Sealed, []byte("lin-1")) {
			t.Error("a stored version holds plaintext state")
		}
		if v.CreatedBy != "alice@example.com" || v.ComponentRunID == nil || *v.ComponentRunID != apply {
			t.Errorf("version %d attribution = %q / %v", v.Version, v.CreatedBy, v.ComponentRunID)
		}
	}

	// Unlock with another id names the holder (409).
	rec = stateReq(t, h, http.MethodDelete, path+"/lock", applyTok, lockBody("wrong"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("unlock with another id: %d, want 409", rec.Code)
	}

	// Liveness: once the apply step settles, its token is dead.
	if err := f.h.client.ComponentRun.UpdateOneID(apply).SetStatus(componentrun.StatusSucceeded).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec := stateReq(t, h, http.MethodGet, path, applyTok, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("settled step's token: %d, want 401", rec.Code)
	}
}

// TestTofuStateForceUnlock: the lock a dead step left behind can be
// released by `tofu force-unlock` (an unlock carrying only the id) from a
// step with a write token, but not by another step's read token.
func TestTofuStateForceUnlock(t *testing.T) {
	f := newStateFixture(t)
	h := f.h.handler
	path := f.path()
	dead := f.step(t)
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", f.token(t, dead, tofustate.ScopeWrite), lockBody("stuck")); rec.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body)
	}

	force := []byte(`{"ID":"stuck"}`)
	reader := f.step(t)
	if rec := stateReq(t, h, http.MethodDelete, path+"/lock", f.token(t, reader, tofustate.ScopeRead), force); rec.Code != http.StatusForbidden {
		t.Fatalf("read token force-unlock: %d, want 403", rec.Code)
	}
	op := f.step(t)
	opTok := f.token(t, op, tofustate.ScopeWrite)
	if rec := stateReq(t, h, http.MethodDelete, path+"/lock", opTok, force); rec.Code != http.StatusOK {
		t.Fatalf("force-unlock: %d %s", rec.Code, rec.Body)
	}
	// Unlocking an unlocked state is fine; a lock is free again.
	if rec := stateReq(t, h, http.MethodDelete, path+"/lock", opTok, force); rec.Code != http.StatusOK {
		t.Fatalf("second unlock: %d %s", rec.Code, rec.Body)
	}
	if rec := stateReq(t, h, http.MethodPost, path+"/lock", opTok, lockBody("fresh")); rec.Code != http.StatusOK {
		t.Fatalf("lock after release: %d %s", rec.Code, rec.Body)
	}
}

// TestTofuStateTenancy: the state is looked up from the token's claims, and
// a token whose claims don't match its step (another organization or
// application) is refused, as is a token whose run belongs elsewhere.
func TestTofuStateTenancy(t *testing.T) {
	f := newStateFixture(t)
	h := f.h.handler
	step := f.step(t)
	for name, mut := range map[string]func(c *tofustate.Claims){
		"another org":         func(c *tofustate.Claims) { c.OrgID = uuid.New() },
		"another application": func(c *tofustate.Claims) { c.ApplicationID = uuid.New() },
		"another run":         func(c *tofustate.Claims) { c.WorkflowRunID = uuid.New() },
	} {
		c := tofustate.Claims{
			OrgID: f.orgID, ApplicationID: f.appID, ComponentID: f.component, Workspace: "default",
			WorkflowRunID: f.runID, ComponentRunID: step, Scope: tofustate.ScopeWrite,
			Expiry: time.Now().Add(time.Hour).Unix(),
		}
		mut(&c)
		tok, err := f.signer.Sign(c)
		if err != nil {
			t.Fatal(err)
		}
		if rec := stateReq(t, h, http.MethodGet, f.path(), tok, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
	}
}

// TestTofuStateBodyLimit: an upload over the configured limit is a 413.
func TestTofuStateBodyLimit(t *testing.T) {
	f := newStateFixture(t)
	sealer := mustSealer(t)
	h := newTestHandler(ServerDeps{TofuState: tofustate.NewService(f.h.client, sealer, f.signer), TofuStateMaxBytes: 64})
	step := f.step(t)
	tok := f.token(t, step, tofustate.ScopeWrite)
	if rec := stateReq(t, h, http.MethodPost, f.path()+"/lock", tok, lockBody("l")); rec.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body)
	}
	if rec := stateReq(t, h, http.MethodPost, f.path()+"?ID=l", tok, stateBody("lin", 1, "a much longer marker than fits")); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized state: %d, want 413", rec.Code)
	}
}

func mustSealer(t *testing.T) *secrets.Sealer {
	t.Helper()
	s, err := secrets.NewSealer(testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTofuStateLockContention: steps racing for the lock of a brand-new
// state (the row is created by the first lock) — exactly one wins, every
// other gets a 423 naming it.
func TestTofuStateLockContention(t *testing.T) {
	f := newStateFixture(t)
	h := f.h.handler
	const racers = 8
	toks := make([]string, racers)
	for i := range toks {
		toks[i] = f.token(t, f.step(t), tofustate.ScopeRead)
	}
	codes := make(chan int, racers)
	start := make(chan struct{})
	for i, tok := range toks {
		go func() {
			<-start
			rec := stateReq(t, h, http.MethodPost, f.path()+"/lock", tok, lockBody(fmt.Sprintf("lock-%d", i)))
			codes <- rec.Code
		}()
	}
	close(start)
	won, locked := 0, 0
	for range racers {
		switch <-codes {
		case http.StatusOK:
			won++
		case http.StatusLocked:
			locked++
		}
	}
	if won != 1 || locked != racers-1 {
		t.Fatalf("won %d, locked %d; want exactly one winner and %d refusals", won, locked, racers-1)
	}
}
