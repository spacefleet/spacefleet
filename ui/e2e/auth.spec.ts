import { test, expect } from "@playwright/test";

// The critical cross-cutting journey: a real browser logging in through Dex and
// landing in an organization. Covers the bug class unit/integration tests can't
// see — most notably the post-login callback routing (must land in-app, never
// NotFound) and the org gate redirect for a fresh user.
test("log in via Dex, set up an organization, land on Home, sign out", async ({
  page,
}) => {
  // Unauthenticated visit is auto-redirected to the Dex login form.
  await page.goto("/");
  await page.waitForURL(/localhost:5556\/dex\/auth/);

  // Seeded dev credentials (dev/dex/config.yaml).
  await page.locator("#login").fill("admin@example.com");
  await page.locator("#password").fill("password");
  await page.locator("#submit-login").click();

  // Back in the app — NOT the NotFound page. This is the regression guard for
  // the callback-routing bug.
  await page.waitForURL(/localhost:2424\//);
  await expect(page.getByText("No route matched")).toHaveCount(0);

  // A brand-new user has no organization, so the org gate sends them to the
  // create-org screen. On repeat runs the org already exists and we land in the
  // app directly — handle both so the test is idempotent against a shared DB.
  const orgNameField = page.getByPlaceholder("Organization name");
  if (await orgNameField.isVisible().catch(() => false)) {
    await orgNameField.fill(`E2E Org ${Date.now()}`);
    await page.getByRole("button", { name: "Create organization" }).click();
  }

  // We're in the app: the org switcher is visible in the top bar.
  await expect(
    page.getByRole("button", { name: /Select organization|E2E Org/ }),
  ).toBeVisible();

  // Sign out clears the session and returns us to the Dex login form.
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.locator("#login")).toBeVisible();
});
