package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestTofuStateUnconfigured: without managed state (no secret key) every
// state route answers 503, and the routes sit outside the Dex auth chain —
// a request with no bearer token reaches the handler rather than a 401 from
// RequireAuth.
func TestTofuStateUnconfigured(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(ServerDeps{})
	path := "/api/tofu/state/" + uuid.NewString() + "/default"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path + "?ID=x"},
		{http.MethodPost, path + "/lock"},
		{http.MethodDelete, path + "/lock"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"ID":"x"}`))
		req.SetBasicAuth("spacefleet", "token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}
