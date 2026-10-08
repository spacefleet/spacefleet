import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";

// Same-origin fetch: in dev, Vite proxies /api/* to the Go server; in prod,
// the Go binary serves both the SPA and /api/* from the same origin.
export const api = createClient<paths>({ baseUrl: "/" });

// ApiAuthBinder (see components/ApiAuthBinder.tsx) wires this up. Before the
// user signs in it returns null, so requests go out without a bearer token and
// the backend rejects protected routes with 401. /api/health is always public.
let tokenProvider: (() => Promise<string | null>) | null = null;

export function setAuthTokenProvider(
  fn: (() => Promise<string | null>) | null,
) {
  tokenProvider = fn;
}

// ApiAuthBinder wires this up too. It's called with the rejected token when the
// backend answers 401 to a request that carried one: the stored session is no
// longer valid (e.g. Dex restarted and rotated its signing keys), so the binder
// drops it and AuthGate routes to /login rather than leaving the app sending a
// dead token until it expires.
let unauthorizedHandler: ((token: string) => void) | null = null;

export function setUnauthorizedHandler(fn: ((token: string) => void) | null) {
  unauthorizedHandler = fn;
}

const authMiddleware: Middleware = {
  async onRequest({ request }) {
    if (!tokenProvider) return;
    const token = await tokenProvider();
    if (token) request.headers.set("Authorization", `Bearer ${token}`);
  },
  onResponse({ request, response }) {
    if (response.status !== 401) return;
    const auth = request.headers.get("Authorization");
    if (auth?.startsWith("Bearer ")) {
      unauthorizedHandler?.(auth.slice("Bearer ".length));
    }
  },
};

api.use(authMiddleware);

// OrgProvider (see contexts/OrgContext.tsx) wires this up. It returns the
// currently selected organization id, sent as X-Organization-ID so org-scoped
// endpoints know which tenant the request targets. Null until an org is
// selected (or for the bootstrap /api/me call, which is org-agnostic).
let orgProvider: (() => string | null) | null = null;

export function setOrgProvider(fn: (() => string | null) | null) {
  orgProvider = fn;
}

const orgMiddleware: Middleware = {
  onRequest({ request }) {
    if (!orgProvider) return;
    const orgId = orgProvider();
    if (orgId) request.headers.set("X-Organization-ID", orgId);
  },
};

api.use(orgMiddleware);

// Streaming endpoints (Server-Sent Events) are consumed over fetch rather than
// the openapi-fetch client, so they can't go through the middleware above.
// These accessors expose the same token/org sources so a stream can send the
// identical Authorization + X-Organization-ID headers (see lib/useResourceStream).
export async function authToken(): Promise<string | null> {
  return tokenProvider ? tokenProvider() : null;
}

export function currentOrgId(): string | null {
  return orgProvider ? orgProvider() : null;
}
