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

  // After login the app settles into one of two terminal states: a brand-new
  // user with no organization lands on the create-org screen (the org gate
  // sends them there); a user who already belongs to an org lands in the app
  // with the org switcher in the top bar. Both are valid, so the test handles
  // either — but it must WAIT for one of them to appear first. The post-login
  // callback is still being processed (token exchange, the /api/me fetch) when
  // waitForURL resolves, so a bare isVisible() check here races and reports the
  // create-org form as absent, stranding the test on that screen.
  const orgNameField = page.getByPlaceholder("Organization name");
  const orgSwitcher = page.getByRole("button", {
    name: /Select organization|E2E Org/,
  });
  await expect(orgNameField.or(orgSwitcher).first()).toBeVisible();

  // Fresh user: fill in the org and create it. (An existing-org user skips
  // this and is already looking at the switcher.)
  if (await orgNameField.isVisible()) {
    await orgNameField.fill(`E2E Org ${Date.now()}`);
    await page.getByRole("button", { name: "Create organization" }).click();
  }

  // We're in the app: the org switcher is visible in the top bar.
  await expect(orgSwitcher).toBeVisible();

  // Sign out clears the session and returns us to the Dex login form.
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.locator("#login")).toBeVisible();
});
