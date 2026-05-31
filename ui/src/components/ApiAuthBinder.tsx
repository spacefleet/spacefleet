import { Outlet } from "react-router";
import { useAuth } from "react-oidc-context";
import { setAuthTokenProvider } from "../api/client";

// Installs the bearer-token provider that the API client (src/api/client.ts)
// attaches to every request. It returns the current Dex-issued ID token, which
// the Go backend verifies (see lib/auth/oidc.go). Returns null when there's no
// session yet, so requests go out unauthenticated and the backend answers 401.
//
// The provider reads from a ref-like closure over `auth` rather than capturing
// a single token, so it always sends the freshest token (e.g. after a silent
// renew) without needing to re-run.
//
// It's set during render — not in useEffect — so it's in place before any
// descendant's mount effects fire (child effects run before parent effects in
// React, so effect-based wiring would race the first API call from a child).
export function ApiAuthBinder() {
  const auth = useAuth();
  setAuthTokenProvider(async () => auth.user?.id_token ?? null);
  return <Outlet />;
}
