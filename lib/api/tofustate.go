package api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/tofustate"
)

// DefaultTofuStateMaxBytes caps a state upload when no limit is configured
// (TOFU_STATE_MAX_BYTES).
const DefaultTofuStateMaxBytes = 64 << 20

// maxLockBody bounds a lock or unlock request; LockInfo is a few hundred
// bytes.
const maxLockBody = 64 << 10

// tofuStateIOTimeout replaces the server's short read/write deadlines for a
// state request: a large state over a slow link needs longer than an API
// call.
const tofuStateIOTimeout = 5 * time.Minute

// The managed OpenTofu state endpoints: the server side of OpenTofu's `http`
// backend (see lib/tofustate). They are public routes, mounted outside the
// Dex auth chain (lib/server/routes.go), because the callers are runner
// pods, not browsers, and the protocol isn't JSON-API shaped (raw state
// bodies, a 423 carrying the lock holder, Content-MD5). Each request is
// authenticated by the per-step state token sent as the basic-auth password.
//
//	GET    /api/tofu/state/{componentId}/{workspace}       → 200 state | 204 none
//	POST   /api/tofu/state/{componentId}/{workspace}?ID=   → 200 stored
//	POST   /api/tofu/state/{componentId}/{workspace}/lock  → 200 | 423 holder
//	DELETE /api/tofu/state/{componentId}/{workspace}/lock  → 200 | 409 holder

// GetTofuState returns the current state of a component workspace, or 204
// when nothing has been written yet.
func (s *Server) GetTofuState(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.tofuStateClaims(w, r)
	if !ok {
		return
	}
	data, found, err := s.tofuState.Read(r.Context(), claims)
	if err != nil {
		s.tofuStateError(w, r, err)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sum := md5.Sum(data)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// PostTofuState stores a new state version. The request must carry the id of
// the lock its step holds (?ID=); a Content-MD5 header, when sent, must
// match the body.
func (s *Server) PostTofuState(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.tofuStateClaims(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.tofuStateMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "too_large", "the state is larger than this Spacefleet accepts (TOFU_STATE_MAX_BYTES)")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "bad_request", "could not read the state body")
		return
	}
	if raw := r.Header.Get("Content-MD5"); raw != "" {
		want, err := base64.StdEncoding.DecodeString(raw)
		sum := md5.Sum(body)
		if err != nil || !bytes.Equal(want, sum[:]) {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "Content-MD5 does not match the body")
			return
		}
	}
	if err := s.tofuState.Write(r.Context(), claims, r.URL.Query().Get("ID"), body); err != nil {
		s.tofuStateError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// LockTofuState takes the state lock (the backend's lock_method, POST).
func (s *Server) LockTofuState(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.tofuStateClaims(w, r)
	if !ok {
		return
	}
	info, ok := readLockInfo(w, r)
	if !ok {
		return
	}
	if err := s.tofuState.Lock(r.Context(), claims, info); err != nil {
		s.tofuStateError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// UnlockTofuState releases the state lock (the backend's unlock_method,
// DELETE). `tofu force-unlock` sends the same request with only the lock id.
func (s *Server) UnlockTofuState(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.tofuStateClaims(w, r)
	if !ok {
		return
	}
	info, ok := readLockInfo(w, r)
	if !ok {
		return
	}
	if err := s.tofuState.Unlock(r.Context(), claims, info); err != nil {
		s.tofuStateError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// tofuStateClaims is the preamble every state request shares: extend the
// I/O deadlines, then authenticate the basic-auth token against the path's
// component workspace. It writes the error response itself and returns
// ok=false when the request must stop.
func (s *Server) tofuStateClaims(w http.ResponseWriter, r *http.Request) (tofustate.Claims, bool) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(tofuStateIOTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(tofuStateIOTimeout))

	if s.tofuState == nil || !s.tofuState.Enabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "managed OpenTofu state is not configured (set SPACEFLEET_SECRET_KEY)")
		return tofustate.Claims{}, false
	}
	componentID, err := uuid.Parse(r.PathValue("componentId"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown state")
		return tofustate.Claims{}, false
	}
	_, token, ok := r.BasicAuth()
	if !ok || token == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="spacefleet-state"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "a state token is required")
		return tofustate.Claims{}, false
	}
	claims, err := s.tofuState.Authenticate(r.Context(), token, componentID, r.PathValue("workspace"))
	if err != nil {
		s.tofuStateError(w, r, err)
		return tofustate.Claims{}, false
	}
	return claims, true
}

// readLockInfo decodes a lock or unlock request body.
func readLockInfo(w http.ResponseWriter, r *http.Request) (tofustate.LockInfo, bool) {
	var info tofustate.LockInfo
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLockBody))
	if err != nil || json.Unmarshal(body, &info) != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "the lock request body is not valid lock info")
		return info, false
	}
	return info, true
}

// tofuStateError maps a lib/tofustate error to the protocol's status codes.
// A refused lock answers with the holder's lock info — 423 for a lock, 409
// for an unlock naming another lock — which OpenTofu prints as its usual
// "Error acquiring the state lock" block.
func (s *Server) tofuStateError(w http.ResponseWriter, r *http.Request, err error) {
	var locked *tofustate.LockedError
	switch {
	case errors.As(err, &locked):
		status := http.StatusLocked
		if r.Method == http.MethodDelete {
			status = http.StatusConflict
		}
		writeJSON(w, status, locked.Holder)
	case errors.Is(err, tofustate.ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", `Basic realm="spacefleet-state"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "the state token is invalid, expired, or its step is no longer running")
	case errors.Is(err, tofustate.ErrForbidden):
		writeJSONError(w, http.StatusForbidden, "forbidden", "this state token does not allow that")
	case errors.Is(err, tofustate.ErrBadRequest):
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, tofustate.ErrConflict):
		writeJSONError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, tofustate.ErrDisabled):
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		log.Printf("tofu state: %s %s: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, "internal", "could not process the state request")
	}
}
