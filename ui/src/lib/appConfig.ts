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
