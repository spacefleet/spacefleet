// Vitest setup: register jest-dom's custom matchers (toBeInTheDocument, etc.)
// and clean up the DOM between tests.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
});

// jsdom lacks ResizeObserver / matchMedia / DOMMatrix, which React Flow uses to
// measure the canvas. Stub them so the workflow canvas renders in tests (we test
// our wiring, not React Flow's layout).
if (!("ResizeObserver" in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver =
    ResizeObserverStub as unknown as typeof ResizeObserver;
}

if (typeof window !== "undefined" && !window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

if (!("DOMMatrixReadOnly" in globalThis)) {
  class DOMMatrixStub {
    m22 = 1;
    constructor() {}
  }
  // React Flow reads transforms via DOMMatrixReadOnly when measuring.
  globalThis.DOMMatrixReadOnly =
    DOMMatrixStub as unknown as typeof DOMMatrixReadOnly;
}
