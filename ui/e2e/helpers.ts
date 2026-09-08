import { expect, type Page } from "@playwright/test";

// loginIntoOrg logs in through the dev Dex and makes sure the session is
// inside an organization (creating one when the user has none), mirroring
// the auth journey. Returns once the app chrome (org switcher) is up.
export async function loginIntoOrg(page: Page) {
  await page.goto("/");
  await page.waitForURL(/localhost:2424\/login/);
  await page
    .getByRole("button", { name: "Continue with Email and password" })
    .click();

  await page.waitForURL(/localhost:2424\/dex\/auth/);
  await page.locator("#login").fill("admin@example.com");
  await page.locator("#password").fill("password");
  await page.locator("#submit-login").click();

  await page.waitForURL(/localhost:2424\//);
  const orgNameField = page.getByPlaceholder("Organization name");
  // The switcher shows whatever organization the user already belongs to
  // (a reused dev database has one), so find it by test id, not by name.
  const orgSwitcher = page.getByTestId("org-switcher");
  await expect(orgNameField.or(orgSwitcher).first()).toBeVisible();
  if (await orgNameField.isVisible()) {
    await orgNameField.fill(`E2E Org ${Date.now()}`);
    await page.getByRole("button", { name: "Create organization" }).click();
  }
  await expect(orgSwitcher).toBeVisible();
}
