import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { APP_TITLE, documentTitle, useDocumentTitle } from "./useDocumentTitle";

describe("documentTitle", () => {
  it("leads with the most specific part and ends with the app name", () => {
    expect(documentTitle("api", "Applications")).toBe(
      "api · Applications · Spacefleet",
    );
  });

  it("drops parts that aren't loaded yet", () => {
    expect(documentTitle(undefined, "Clusters")).toBe("Clusters · Spacefleet");
    expect(documentTitle(null, false, "")).toBe(APP_TITLE);
  });
});

describe("useDocumentTitle", () => {
  beforeEach(() => {
    document.title = APP_TITLE;
  });

  it("sets the title and follows changes", () => {
    const { rerender } = renderHook(
      ({ name }: { name?: string }) => useDocumentTitle(name, "Clusters"),
      { initialProps: {} as { name?: string } },
    );
    expect(document.title).toBe("Clusters · Spacefleet");
    rerender({ name: "prod" });
    expect(document.title).toBe("prod · Clusters · Spacefleet");
  });

  it("restores the bare app title on unmount", () => {
    const { unmount } = renderHook(() => useDocumentTitle("Pods"));
    expect(document.title).toBe("Pods · Spacefleet");
    unmount();
    expect(document.title).toBe(APP_TITLE);
  });
});
