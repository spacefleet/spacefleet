package auth

import (
	"context"
	"net/http"
)

// orgHeader is the request header the SPA sends to indicate which organization
// (tenant) a request targets. It is set by the API client's org middleware
// (ui/src/api/client.ts).
const orgHeader = "X-Organization-ID"

const orgIDKey contextKey = 2

// WithOrgID stores the requested organization id on ctx.
func WithOrgID(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, orgIDKey, orgID)
}

// OrgIDFromContext returns the organization id the request targeted, or
// ("", false) if none was supplied. This is pure plumbing — it does not verify
// the id is valid or that the caller belongs to it; the API layer does that
// authorization check (see Server.currentOrg).
func OrgIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(orgIDKey).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// OrgContext is middleware that lifts the X-Organization-ID header onto the
// request context so handlers can read it via OrgIDFromContext. It performs no
// authorization — endpoints that need an org resolve and check membership
// themselves. Requests without the header pass through untouched (org-agnostic
// endpoints like /api/me don't need one).
func OrgContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get(orgHeader); id != "" {
			r = r.WithContext(WithOrgID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}
