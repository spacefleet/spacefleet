// Helpers for reading the runtime config injected by /config.js into
// window.appConfig (see vite-env.d.ts). Kept tiny and defensive so unit tests
// that don't bootstrap appConfig still get sensible defaults.

// orgCreationEnabled reports whether the SPA should offer organization
// creation. Org creation is on by default, so anything other than an explicit
// `false` counts as enabled — a missing config never locks users out. The
// create endpoint is enforced server-side regardless of this flag.
export function orgCreationEnabled(): boolean {
  return window.appConfig?.allowOrgCreation !== false;
}

// loginMethods returns the sign-in options the login screen should offer,
// mirroring the operator's Dex connectors. Defaults to an empty list when
// config is missing, so the login page falls back to a single generic
// "Sign in" button rather than rendering nothing.
export function loginMethods(): Window["appConfig"]["loginMethods"] {
  return window.appConfig?.loginMethods ?? [];
}

// emailEnabled reports whether the server has SMTP configured. Defaults to
// false when config is missing, so the invite UI errs toward "copy the link
// yourself" rather than implying an email went out.
export function emailEnabled(): boolean {
  return window.appConfig?.emailEnabled === true;
}
