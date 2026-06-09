import { test, expect, type Page } from "@playwright/test";

// Log in through Dex and make sure the session is inside an organization,
// mirroring the auth journey. Returns once the app chrome (org switcher) is up.
async function loginIntoOrg(page: Page) {
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
  const orgSwitcher = page.getByRole("button", {
    name: /Select organization|E2E Org/,
  });
  await expect(orgNameField.or(orgSwitcher).first()).toBeVisible();
  if (await orgNameField.isVisible()) {
    await orgNameField.fill(`E2E Org ${Date.now()}`);
    await page.getByRole("button", { name: "Create organization" }).click();
  }
  await expect(orgSwitcher).toBeVisible();
}

// The application-group (folder) lifecycle, driven through the UI: create a
// group on All Apps, drill into it, rename it, then delete it. Apps aren't
// involved here (registering one needs a Tekton cluster) — moving apps between
// groups is covered by unit/integration tests; this guards the folder CRUD and
// the /applications/groups/:id drill-down routing.
test("create, open, rename, and delete an application group", async ({
  page,
}) => {
  await loginIntoOrg(page);

  // A unique name so reruns don't collide on the per-org unique index.
  const name = `E2E Group ${Date.now()}`;
  const renamed = `${name} (renamed)`;

  await page.goto("/applications");
  await expect(
    page.getByRole("heading", { name: "All Apps" }),
  ).toBeVisible();

  // Create the group via the inline form.
  await page.getByRole("button", { name: "New group" }).click();
  await page.getByPlaceholder("e.g. Backend services").fill(name);
  await page.getByRole("button", { name: "Create", exact: true }).click();

  // The folder appears in the groups list; click into it.
  const folder = page.getByRole("cell", { name }).first();
  await expect(folder).toBeVisible();
  await folder.click();

  // On the group detail page: the heading shows the name and the empty state.
  await expect(page.getByRole("heading", { name })).toBeVisible();
  await expect(
    page.getByText("No applications in this group yet", { exact: false }),
  ).toBeVisible();

  // Rename it.
  await page.getByRole("button", { name: "Rename" }).click();
  const input = page.getByRole("textbox", { name: "Group name" });
  await input.fill(renamed);
  await page.getByRole("button", { name: "Save name" }).click();
  await expect(page.getByRole("heading", { name: renamed })).toBeVisible();

  // Delete it — the confirm dialog explains apps fall back to the root.
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(
    page.getByRole("heading", { name: `Delete ${renamed}` }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Delete group" }).click();

  // Back on All Apps; the deleted group is gone.
  await expect(
    page.getByRole("heading", { name: "All Apps" }),
  ).toBeVisible();
  await expect(page.getByRole("cell", { name: renamed })).toHaveCount(0);
});
