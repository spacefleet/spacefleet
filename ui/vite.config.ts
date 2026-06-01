/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In dev, Vite runs on :2424 and proxies /api/*, /config.js, and /dex/* to the
// Go server on :8080 (the Go server in turn reverse-proxies /dex to the Dex
// container on :5556). This keeps the whole login flow same-origin in dev, just
// like prod. In prod, `vite build` writes to ./dist, which is embedded into the
// Go binary (see ./embed.go) and served from the same origin — no proxy needed.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    // Fixed dev port. strictPort fails loudly if 2424 is taken rather than
    // silently moving to another port (which would break the Dex redirect URI
    // and allowed-origin entries pinned to :2424 in dev/dex/config.yaml).
    port: 2424,
    strictPort: true,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: false,
      },
      // `/config.js` is served by the Go backend and exposes `window.appConfig`
      // (see lib/server/routes.go). Proxy it so the dev server gets real values.
      "/config.js": {
        target: "http://localhost:8080",
        changeOrigin: false,
      },
      // `/dex/*` is the bundled identity provider, reverse-proxied by the Go
      // backend (DEX_UPSTREAM_URL). Proxying it here keeps Dex same-origin with
      // the SPA so the OIDC flow needs no CORS.
      "/dex": {
        target: "http://localhost:8080",
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  // Vitest: jsdom for component tests, globals so describe/it/expect don't need
  // importing, and a setup file that wires up jest-dom matchers.
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // Vitest owns *.test.* under src; Playwright owns e2e/*.spec.ts. Scope the
    // include so Vitest never tries to run the Playwright specs.
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
