import { test, expect } from "@playwright/test";
import { loginIntoOrg } from "./helpers";

// The plan-policy lifecycle through the UI and the real API: a policy whose
// Rego does not compile is refused with the compiler's message; a valid one
// is saved, listed, toggled off, and deleted. Evaluating a policy against a
// plan needs a run on a Tekton cluster and is covered by the Go integration
// tests.
test("add, toggle, and delete a plan policy", async ({ page }) => {
  await loginIntoOrg(page);
  const name = `E2E policy ${Date.now()}`;

  await page.goto("/admin/policies");
  await expect(page.getByRole("heading", { name: "Policies" })).toBeVisible();
  await page.getByRole("button", { name: "Add policy" }).click();
  await page.getByLabel("Name").fill(name);

  // A policy in the wrong package is refused before anything is stored.
  await page.getByLabel("Rego").fill("package other\n\ndeny contains msg if { msg := \"x\" }\n");
  await page.getByRole("button", { name: "Save policy" }).click();
  await expect(page.getByText(/package must be "spacefleet"/)).toBeVisible();

  await page
    .getByLabel("Rego")
    .fill("package spacefleet\n\ndeny contains msg if {\n\tinput.plan.destroy > 0\n\tmsg := \"no destroys\"\n}\n");
  await page.getByLabel("Enforcement").selectOption("warn");
  await page.getByRole("button", { name: "Save policy" }).click();

  const row = page.getByRole("row", { name: new RegExp(name) });
  await expect(row).toBeVisible();
  await expect(row).toContainText("warn");
  await expect(row).toContainText("all applications");

  const toggle = row.getByLabel(`Enable ${name}`);
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();

  page.once("dialog", (d) => d.accept());
  await row.getByLabel(`Delete ${name}`).click();
  await expect(page.getByRole("row", { name: new RegExp(name) })).toHaveCount(0);
});
