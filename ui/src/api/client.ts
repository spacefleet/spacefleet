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

const authMiddleware: Middleware = {
  async onRequest({ request }) {
    if (!tokenProvider) return;
    const token = await tokenProvider();
    if (token) request.headers.set("Authorization", `Bearer ${token}`);
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
