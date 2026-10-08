import { useEffect } from "react";

// APP_TITLE is the bare window title (also index.html's static <title>), shown
// whenever no page has set a more specific one.
export const APP_TITLE = "Spacefleet";

// documentTitle joins title parts most-specific first, ending with the app
// name: ("api", "Applications") → "api · Applications · Spacefleet". Empty
// parts drop out, so a page can pass a name that's still loading.
export function documentTitle(
  ...parts: (string | null | undefined | false)[]
): string {
  return [...parts.filter(Boolean), APP_TITLE].join(" · ");
}

// useDocumentTitle sets the browser window/tab title while the calling page is
// mounted, and restores the bare app title when it unmounts (so a route that
// sets none doesn't inherit a stale one). Call it from the page component a
// route renders — one caller per route, since a parent's effect would run after
// (and overwrite) its child's.
export function useDocumentTitle(
  ...parts: (string | null | undefined | false)[]
): void {
  const title = documentTitle(...parts);
  useEffect(() => {
    document.title = title;
  }, [title]);
  useEffect(
    () => () => {
      document.title = APP_TITLE;
    },
    [],
  );
}
