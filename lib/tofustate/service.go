// Package tofustate is Spacefleet's managed OpenTofu state: the server side
// of OpenTofu's `http` backend for components whose backend is
// "spacefleet". State is kept in Postgres, versioned (every accepted write is
// a new, append-only version) and sealed with the deployment's secret key.
//
// Callers are runner pods, not browsers. Each OpenTofu execution step gets
// its own short-lived token (see Signer), minted by the worker when it plans
// the step and handed to the pod as TF_HTTP_PASSWORD. Every request is
// checked three ways: the token's signature and expiry, that the path names
// the component workspace the token was minted for, and that the step it was
// minted for is still running — so a settled run's tokens are dead even when
// a zombie pod is still going. The state row is then looked up from the
// token's claims (organization, application, component, workspace), never
// from the path alone.
//
// Locks are taken with a conditional UPDATE and writes are guarded by an
// optimistic version check, so the database is the only arbiter across
// serve replicas.
package tofustate

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/predicate"
	"github.com/spacefleet/spacefleet/ent/schema"
	tstate "github.com/spacefleet/spacefleet/ent/tofustate"
	tsversion "github.com/spacefleet/spacefleet/ent/tofustateversion"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// DefaultWorkspace is the workspace a component without a workspace
// setting uses.
const DefaultWorkspace = "default"

// Username is the basic-auth user name the backend sends (TF_HTTP_USERNAME).
// Only the password — the token — is checked.
const Username = "spacefleet"

// LockInfo is OpenTofu's lock description as the `http` backend sends it.
type LockInfo = schema.TofuLockInfo

// Errors the handlers map to protocol status codes.
var (
	// ErrForbidden: the token is valid but may not do this (a read token
	// writing, or a path that isn't the token's component workspace) → 403.
	ErrForbidden = errors.New("tofustate: not allowed for this token")
	// ErrBadRequest wraps a malformed body → 400.
	ErrBadRequest = errors.New("tofustate: bad request")
	// ErrConflict wraps a refused write (not locked by the caller, a lineage
	// change, an older serial) → 409.
	ErrConflict = errors.New("tofustate: conflict")
	// ErrDisabled: no secret key is configured → 503.
	ErrDisabled = errors.New("tofustate: managed state is not configured (set SPACEFLEET_SECRET_KEY)")
)

// LockedError reports the lock that refused a lock or unlock request. The
// handler returns Holder as the body (423 for a lock, 409 for an unlock with
// another lock's id), which OpenTofu prints as its usual lock error.
type LockedError struct {
	Holder LockInfo
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("tofustate: state is locked (lock id %s)", e.Holder.ID)
}

// Service serves managed state. sealer and signer must be enabled for it to
// do anything (see Enabled).
type Service struct {
	ent    *ent.Client
	sealer *secrets.Sealer
	signer *Signer
	now    func() time.Time
}

// NewService builds the managed-state service. A nil signer (no secret key)
// or a disabled sealer leaves it disabled: every call returns ErrDisabled.
func NewService(entClient *ent.Client, sealer *secrets.Sealer, signer *Signer) *Service {
	return &Service{ent: entClient, sealer: sealer, signer: signer, now: time.Now}
}

// Enabled reports whether managed state can be served.
func (s *Service) Enabled() bool {
	return s != nil && s.ent != nil && s.signer != nil && s.sealer != nil && s.sealer.Enabled()
}

// Address is the state URL OpenTofu's `http` backend uses for a component
// workspace, under base (the URL runner pods reach this Spacefleet at). The
// lock and unlock address is Address + "/lock".
func Address(base string, componentID uuid.UUID, workspace string) string {
	return strings.TrimRight(base, "/") + "/api/tofu/state/" + componentID.String() + "/" + url.PathEscape(WorkspaceName(workspace))
}

// WorkspaceName maps a component's workspace setting to the state's
// workspace ("" is the default workspace).
func WorkspaceName(ws string) string {
	if ws == "" {
		return DefaultWorkspace
	}
	return ws
}

// Authenticate verifies a state token for a request on the given component
// workspace and returns its claims: the signature and expiry, the path
// against the claims, and the liveness of the step the token was minted for
// (its component run must still be running, in the claimed workflow run of
// the claimed application). Any failure of the token itself is
// ErrInvalidToken; a path that isn't the token's is ErrForbidden.
func (s *Service) Authenticate(ctx context.Context, token string, componentID uuid.UUID, workspace string) (Claims, error) {
	if !s.Enabled() {
		return Claims{}, ErrDisabled
	}
	c, err := s.signer.Verify(token, s.now())
	if err != nil {
		return Claims{}, err
	}
	if c.ComponentID != componentID || WorkspaceName(c.Workspace) != workspace {
		return Claims{}, ErrForbidden
	}
	live, err := s.ent.ComponentRun.Query().
		Where(
			componentrun.OrganizationID(c.OrgID),
			componentrun.ID(c.ComponentRunID),
			componentrun.WorkflowRunID(c.WorkflowRunID),
			componentrun.StatusEQ(componentrun.StatusRunning),
			componentrun.HasWorkflowRunWith(
				workflowrun.OrganizationID(c.OrgID),
				workflowrun.ApplicationID(c.ApplicationID),
			),
		).
		Exist(ctx)
	if err != nil {
		return Claims{}, err
	}
	if !live {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// predicateState is a tofu_states query predicate.
type predicateState = predicate.TofuState

// statePredicates scopes a tofu_states query to the claims' component
// workspace — the tenancy boundary: organization, application, component,
// workspace, all from the token.
func statePredicates(c Claims) []predicateState {
	return []predicateState{
		tstate.OrganizationID(c.OrgID),
		tstate.ApplicationID(c.ApplicationID),
		tstate.ComponentID(c.ComponentID),
		tstate.Workspace(WorkspaceName(c.Workspace)),
	}
}

// findState returns the claims' state row, or nil when none exists yet.
func (s *Service) findState(ctx context.Context, c Claims) (*ent.TofuState, error) {
	st, err := s.ent.TofuState.Query().Where(statePredicates(c)...).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return st, err
}

// ensureState returns the claims' state row, creating it (unlocked, no
// versions) on first use. Two first requests racing both end up with the
// one row: the loser's insert hits the unique index and re-reads.
func (s *Service) ensureState(ctx context.Context, c Claims) (*ent.TofuState, error) {
	st, err := s.findState(ctx, c)
	if err != nil || st != nil {
		return st, err
	}
	created, err := s.ent.TofuState.Create().
		SetOrganizationID(c.OrgID).
		SetApplicationID(c.ApplicationID).
		SetComponentID(c.ComponentID).
		SetWorkspace(WorkspaceName(c.Workspace)).
		Save(ctx)
	if err == nil {
		return created, nil
	}
	if !ent.IsConstraintError(err) {
		return nil, err
	}
	return s.ent.TofuState.Query().Where(statePredicates(c)...).Only(ctx)
}

// Read returns the current state, or ok=false when nothing has been written
// yet (the backend's "no state" answer).
func (s *Service) Read(ctx context.Context, c Claims) (data []byte, ok bool, err error) {
	if !s.Enabled() {
		return nil, false, ErrDisabled
	}
	st, err := s.findState(ctx, c)
	if err != nil || st == nil || st.CurrentVersion == 0 {
		return nil, false, err
	}
	v, err := s.ent.TofuStateVersion.Query().
		Where(
			tsversion.OrganizationID(c.OrgID),
			tsversion.StateID(st.ID),
			tsversion.Version(st.CurrentVersion),
		).
		Only(ctx)
	if err != nil {
		return nil, false, err
	}
	data, err = s.open(v.Sealed)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// lockAttempts bounds Lock's retry when the lock is released between the
// refused update and the read of its holder.
const lockAttempts = 3

// Lock takes the state lock for the claims' step. Taking a lock the step
// already holds (same id) succeeds again; a lock held by anything else is a
// *LockedError carrying the holder's lock info. The row is created on first
// use, so a brand-new workspace can be locked before anything is written.
func (s *Service) Lock(ctx context.Context, c Claims, info LockInfo) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if info.ID == "" {
		return fmt.Errorf("%w: the lock request has no ID", ErrBadRequest)
	}
	st, err := s.ensureState(ctx, c)
	if err != nil {
		return err
	}
	for range lockAttempts {
		n, err := s.ent.TofuState.Update().
			Where(
				tstate.ID(st.ID),
				tstate.Or(
					tstate.LockIDIsNil(),
					// Re-locking is idempotent only for the step that holds the
					// lock: a lock id read from a 423 body can't be used to take
					// over someone else's lock.
					tstate.And(tstate.LockID(info.ID), tstate.LockedByComponentRunID(c.ComponentRunID)),
				),
			).
			SetLockID(info.ID).
			SetLockInfo(&info).
			SetLockedAt(s.now()).
			SetLockedByComponentRunID(c.ComponentRunID).
			Save(ctx)
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		cur, err := s.ent.TofuState.Get(ctx, st.ID)
		if err != nil {
			return err
		}
		if cur.LockID != nil {
			return &LockedError{Holder: holderOf(cur)}
		}
		// Released between the update and the read: try again.
	}
	return fmt.Errorf("%w: the lock is changing hands; try again", ErrConflict)
}

// Unlock releases the lock with the given id. A step releases its own lock;
// releasing a lock another step holds (`tofu force-unlock`) needs a write
// token, so a plan step can't break another run's lock. Unlocking a state
// that is not locked succeeds. A different lock id is a *LockedError with
// the holder's info.
func (s *Service) Unlock(ctx context.Context, c Claims, info LockInfo) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if info.ID == "" {
		return fmt.Errorf("%w: the unlock request has no ID", ErrBadRequest)
	}
	st, err := s.findState(ctx, c)
	if err != nil || st == nil {
		return err
	}
	preds := []predicateState{tstate.ID(st.ID), tstate.LockID(info.ID)}
	if !c.CanWrite() {
		preds = append(preds, tstate.LockedByComponentRunID(c.ComponentRunID))
	}
	n, err := s.ent.TofuState.Update().
		Where(preds...).
		ClearLockID().
		ClearLockInfo().
		ClearLockedAt().
		ClearLockedByComponentRunID().
		Save(ctx)
	if err != nil || n == 1 {
		return err
	}
	cur, err := s.ent.TofuState.Get(ctx, st.ID)
	if err != nil {
		return err
	}
	switch {
	case cur.LockID == nil:
		return nil
	case *cur.LockID != info.ID:
		return &LockedError{Holder: holderOf(cur)}
	default:
		// The right id, but another step's lock and only a read token.
		return ErrForbidden
	}
}

// stateHeader is the part of a state file the write guards read.
type stateHeader struct {
	Version int    `json:"version"`
	Serial  *int64 `json:"serial"`
	Lineage string `json:"lineage"`
}

// Write stores body as the new current state. It needs a write token and
// the lock: lockID (the request's ?ID=) must be the lock this step holds —
// Spacefleet's scripts never pass -lock=false, so an unlocked write is
// refused. On existing state the lineage must not change (another root
// module's state) and the serial must not go backwards; the same serial is
// accepted only as a byte-identical retry, which is a no-op.
func (s *Service) Write(ctx context.Context, c Claims, lockID string, body []byte) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if !c.CanWrite() {
		return ErrForbidden
	}
	var head stateHeader
	if err := json.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("%w: the state is not valid JSON", ErrBadRequest)
	}
	if head.Lineage == "" || head.Serial == nil {
		return fmt.Errorf("%w: the state has no lineage or serial", ErrBadRequest)
	}
	sum := md5.Sum(body)
	digest := hex.EncodeToString(sum[:])

	st, err := s.findState(ctx, c)
	if err != nil {
		return err
	}
	if st == nil || lockID == "" || st.LockID == nil || *st.LockID != lockID ||
		st.LockedByComponentRunID == nil || *st.LockedByComponentRunID != c.ComponentRunID {
		return fmt.Errorf("%w: the state is not locked by this request", ErrConflict)
	}
	if st.CurrentVersion > 0 {
		if head.Lineage != st.Lineage {
			return fmt.Errorf("%w: the state's lineage %q does not match the stored lineage %q (is this another root module's state?)", ErrConflict, head.Lineage, st.Lineage)
		}
		switch serial := *head.Serial; {
		case serial < st.Serial:
			return fmt.Errorf("%w: serial %d is older than the stored serial %d", ErrConflict, serial, st.Serial)
		case serial == st.Serial:
			cur, err := s.ent.TofuStateVersion.Query().
				Where(tsversion.OrganizationID(c.OrgID), tsversion.StateID(st.ID), tsversion.Version(st.CurrentVersion)).
				Only(ctx)
			if err != nil {
				return err
			}
			if cur.Md5 == digest {
				return nil // a retry of the write that is already stored
			}
			return fmt.Errorf("%w: serial %d is already stored with different contents", ErrConflict, serial)
		}
	}

	sealed, err := s.seal(body)
	if err != nil {
		return err
	}
	startedBy := ""
	if run, err := s.ent.WorkflowRun.Query().
		Where(workflowrun.OrganizationID(c.OrgID), workflowrun.ID(c.WorkflowRunID)).
		Only(ctx); err == nil {
		startedBy = run.StartedBy
	}

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return err
	}
	next := st.CurrentVersion + 1
	if err := tx.TofuStateVersion.Create().
		SetStateID(st.ID).
		SetOrganizationID(c.OrgID).
		SetVersion(next).
		SetSerial(*head.Serial).
		SetLineage(head.Lineage).
		SetSealed(sealed).
		SetSizeBytes(int64(len(body))).
		SetMd5(digest).
		SetWorkflowRunID(c.WorkflowRunID).
		SetComponentRunID(c.ComponentRunID).
		SetCreatedBy(startedBy).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return fmt.Errorf("%w: a concurrent write stored version %d first", ErrConflict, next)
		}
		return err
	}
	// Optimistic guard: the pointer only moves from the version this write
	// was checked against, and only while the caller still holds the lock.
	n, err := tx.TofuState.Update().
		Where(
			tstate.ID(st.ID),
			tstate.CurrentVersion(st.CurrentVersion),
			tstate.LockID(lockID),
			tstate.LockedByComponentRunID(c.ComponentRunID),
		).
		SetCurrentVersion(next).
		SetSerial(*head.Serial).
		SetLineage(head.Lineage).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n != 1 {
		_ = tx.Rollback()
		return fmt.Errorf("%w: the state changed or was unlocked while writing", ErrConflict)
	}
	return tx.Commit()
}

// holderOf returns a locked row's lock info, falling back to the bare id.
func holderOf(st *ent.TofuState) LockInfo {
	if st.LockInfo != nil {
		return *st.LockInfo
	}
	var h LockInfo
	if st.LockID != nil {
		h.ID = *st.LockID
	}
	return h
}

// seal gzips then seals a state body.
func (s *Service) seal(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return s.sealer.Seal(buf.Bytes())
}

// open reverses seal.
func (s *Service) open(sealed []byte) ([]byte, error) {
	compressed, err := s.sealer.Open(sealed)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("tofustate: decompress state: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}
