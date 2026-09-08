import { test, expect } from "@playwright/test";
import { loginIntoOrg } from "./helpers";

// The notification-channel lifecycle through the UI and the real API: an
// email channel with a chosen event set is added and listed with its
// destination, then deleted. Delivery needs the worker and an SMTP server
// and is covered by the Go integration tests.
test("add and delete a notification channel", async ({ page }) => {
  await loginIntoOrg(page);
  const name = `E2E channel ${Date.now()}`;

  await page.goto("/admin/notifications");
  await expect(page.getByRole("heading", { name: "Notifications" })).toBeVisible();
  await page.getByRole("button", { name: "Add channel" }).click();
  await page.getByLabel("Name").fill(name);
  await page.getByLabel("Email address").fill("ops@example.com");
  await page.getByLabel("Awaiting approval").uncheck();
  await page.getByRole("button", { name: "Save channel" }).click();

  const row = page.getByRole("row", { name: new RegExp(name) });
  await expect(row).toBeVisible();
  await expect(row).toContainText("ops@example.com");
  await expect(row).toContainText("Run failed, Drift detected");
  await expect(row).toContainText("all applications");

  page.once("dialog", (d) => d.accept());
  await row.getByLabel(`Delete ${name}`).click();
  await expect(page.getByRole("row", { name: new RegExp(name) })).toHaveCount(0);
});
