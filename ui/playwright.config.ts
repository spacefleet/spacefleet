import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against the dev stack: the Vite dev server (:2424)
// serving the SPA and the Go API (:8080) it proxies to. Both are started here
// if not already running (reuseExistingServer), so locally you can just have
// `make dev` + `make ui-dev` going. The webServers do NOT start Postgres /
// Redis / Dex — those must be up first (`make services-up && make migrate-up`).
export default defineConfig({
  testDir: "./e2e",
  // Auth flows have real redirects; give them room but fail fast in CI.
  timeout: 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: "http://localhost:2424",
    trace: "on-first-retry",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
  webServer: [
    {
      // The Go app auto-loads ../.env via godotenv (OIDC_ISSUER etc.).
      command: "go run ./cmd/spacefleet serve",
      cwd: "..",
      url: "http://localhost:8080/api/health",
      reuseExistingServer: true,
      timeout: 120_000,
    },
    {
      command: "npm run dev",
      url: "http://localhost:2424",
      reuseExistingServer: true,
      timeout: 60_000,
    },
  ],
});
