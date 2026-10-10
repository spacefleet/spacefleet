import { test, expect, type Page } from "@playwright/test";
import { loginIntoOrg } from "./helpers";

// The stage builder in a real browser: layout, menus, and drag and drop are
// what jsdom can't check. Registering an application needs a Tekton cluster,
// so the app's workflow endpoints are served from an in-memory workflow here
// (everything else — login, org, clusters — is the real backend). Each PUT is
// recorded so the test asserts what the builder actually saved.

const appId = "00000000-0000-4000-8000-0000000000e2";

interface Stage {
  id: string;
  name: string;
  components: Record<string, unknown>[];
}

function manifest(id: string, name: string) {
  return {
    id,
    name,
    type: "manifest",
    config: {
      repo_url: "https://github.com/acme/manifests.git",
      path: `k8s/${name}`,
    },
    continue_on_failure: false,
    requires_approval: false,
  };
}

async function serveWorkflow(page: Page) {
  let stages: Stage[] = [
    {
      id: "00000000-0000-4000-8000-00000000a001",
      name: "Build",
      components: [
        manifest("00000000-0000-4000-8000-00000000c001", "base"),
        manifest("00000000-0000-4000-8000-00000000c002", "config"),
      ],
    },
    {
      id: "00000000-0000-4000-8000-00000000a002",
      name: "Deploy",
      components: [manifest("00000000-0000-4000-8000-00000000c003", "site")],
    },
  ];
  const saves: Stage[][] = [];
  await page.route(`**/api/applications/${appId}/workflow`, async (route) => {
    if (route.request().method() === "PUT") {
      stages = (route.request().postDataJSON() as { stages: Stage[] }).stages;
      saves.push(stages);
    }
    await route.fulfill({ json: { stages } });
  });
  await page.route(`**/api/applications/${appId}/variables`, (route) =>
    route.fulfill({ json: [] }),
  );
  await page.route(`**/api/applications/${appId}/component-outputs`, (route) =>
    route.fulfill({ json: {} }),
  );
  return saves;
}

const names = (s: Stage) => s.components.map((c) => c.name);

test("stage builder: menus, adding a stage, and moving components", async ({
  page,
}) => {
  await loginIntoOrg(page);
  const saves = await serveWorkflow(page);
  await page.goto(`/applications/${appId}/workflow`);

  const build = page.getByRole("region", { name: "Stage Build" });
  const deploy = page.getByRole("region", { name: "Stage Deploy" });
  await expect(build.getByText("base", { exact: true })).toBeVisible();
  await expect(deploy.getByText("site", { exact: true })).toBeVisible();

  // The add-component menu of the tallest stage opens past the bottom of the
  // builder's scrolling area; its lowest entry must still be clickable (not
  // clipped by it). Picking it opens the editor for a new component there.
  await build.getByRole("button", { name: "Add a component to Build" }).click();
  const tofu = page.getByRole("menuitem", { name: "OpenTofu" });
  await expect(tofu).toBeVisible();
  // Hit-test where it sits: Playwright would otherwise scroll a clipping
  // container to reach it, which hides the bug a person sees.
  expect(
    await tofu.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(
        r.left + r.width / 2,
        r.top + r.height / 2,
      );
      return hit != null && el.contains(hit);
    }),
  ).toBe(true);
  await tofu.click();
  await expect(page).toHaveURL(/\/workflow\/nodes\/.+\/edit\?new=terraform&stage=/);
  await expect(page.getByText(/in stage 1, Build/i)).toBeVisible();
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(build.getByText("opentofu", { exact: true })).toHaveCount(0);

  // Drag "config" from Build onto the lower half of "site" in Deploy, which
  // drops it after site.
  const site = deploy.locator("[draggable=true]", { hasText: "site" });
  const siteBox = await site.boundingBox();
  await build.locator("[draggable=true]", { hasText: "config" }).dragTo(site, {
    targetPosition: { x: 20, y: (siteBox?.height ?? 40) - 4 },
  });
  await expect.poll(() => saves.length).toBeGreaterThan(0);
  let last = saves[saves.length - 1];
  expect(names(last[0])).toEqual(["base"]);
  expect(names(last[1])).toEqual(["site", "config"]);

  // And back again, after base in Build.
  const base = build.locator("[draggable=true]", { hasText: "base" });
  const baseBox = await base.boundingBox();
  await deploy.locator("[draggable=true]", { hasText: "config" }).dragTo(base, {
    targetPosition: { x: 20, y: (baseBox?.height ?? 40) - 4 },
  });
  await expect
    .poll(() => names(saves[saves.length - 1][0]))
    .toEqual(["base", "config"]);

  // Add a stage and rename it in place.
  await page.getByRole("button", { name: "Add stage" }).click();
  const name = page.getByRole("textbox", { name: "Stage name" }).last();
  await name.fill("Verify");
  await name.press("Enter");
  await expect
    .poll(() => saves[saves.length - 1].map((s) => s.name))
    .toEqual(["Build", "Deploy", "Verify"]);
  last = saves[saves.length - 1];
  expect(last[2].components).toEqual([]);
});

test("a stage holding components can't be deleted; an empty one can", async ({
  page,
}) => {
  await loginIntoOrg(page);
  const saves = await serveWorkflow(page);
  await page.goto(`/applications/${appId}/workflow`);

  await page.getByRole("button", { name: "Stage Deploy actions" }).click();
  const item = page.getByRole("menuitem", { name: /delete stage/i });
  await expect(item).toBeDisabled();
  await expect(item).toContainText("Delete or move its components first");
  await page.keyboard.press("Escape");

  await page.getByRole("button", { name: "Add stage" }).click();
  await page.getByRole("button", { name: "Stage Stage 3 actions" }).click();
  await page.getByRole("menuitem", { name: /delete stage/i }).click();
  await expect(page.getByRole("region", { name: "Stage Stage 3" })).toHaveCount(0);
  await expect
    .poll(() => saves[saves.length - 1]?.map((s) => s.name))
    .toEqual(["Build", "Deploy"]);
});

test("deleting a component, leaving what it deployed running", async ({
  page,
}) => {
  await loginIntoOrg(page);
  const saves = await serveWorkflow(page);
  await page.goto(`/applications/${appId}/workflow`);

  await page
    .getByRole("region", { name: "Stage Deploy" })
    .getByText("site", { exact: true })
    .click();
  await expect(page.getByRole("heading", { name: "site" })).toBeVisible();
  await page.getByRole("button", { name: "site actions" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();

  const dialog = page.getByRole("dialog", { name: "Delete site" });
  const del = dialog.getByRole("button", { name: "Delete component" });
  await expect(del).toBeDisabled();
  await dialog.getByRole("radio", { name: /leave it running/i }).check();
  await expect(dialog.getByText(/keeps running on/)).toBeVisible();
  await del.click();

  await expect(page).toHaveURL(new RegExp(`/applications/${appId}/workflow$`));
  await expect(page.getByRole("region", { name: "Stage Deploy" }).getByText("site")).toHaveCount(0);
  expect(saves.length).toBeGreaterThan(0);
  expect(names(saves[saves.length - 1][1])).toEqual([]);
});

test("moving a component to another application", async ({ page }) => {
  const otherId = "00000000-0000-4000-8000-0000000000e3";
  await loginIntoOrg(page);
  await serveWorkflow(page);
  let otherStages: Stage[] = [
    {
      id: "00000000-0000-4000-8000-00000000b001",
      name: "Services",
      components: [manifest("00000000-0000-4000-8000-00000000d001", "api")],
    },
  ];
  await page.route("**/api/applications", (route) =>
    route.fulfill({
      json: [
        { id: appId, name: "shop" },
        { id: otherId, name: "platform" },
      ],
    }),
  );
  await page.route(`**/api/applications/${otherId}/workflow`, (route) =>
    route.fulfill({ json: { stages: otherStages } }),
  );
  for (const path of ["variables", "component-outputs"]) {
    await page.route(`**/api/applications/${otherId}/${path}`, (route) =>
      route.fulfill({ json: path === "variables" ? [] : {} }),
    );
  }
  const site = manifest("00000000-0000-4000-8000-00000000c003", "site");
  let moveBody: Record<string, unknown> | null = null;
  await page.route(`**/api/applications/${appId}/components/*/move`, async (route) => {
    moveBody = route.request().postDataJSON();
    otherStages = [
      ...otherStages,
      { id: "00000000-0000-4000-8000-00000000b002", name: String(moveBody?.new_stage_name), components: [site] },
    ];
    await route.fulfill({ json: site });
  });

  await page.goto(`/applications/${appId}/workflow/nodes/${site.id}`);
  await page.getByRole("button", { name: "site actions" }).click();
  await page.getByRole("menuitem", { name: /move/i }).click();
  const dialog = page.getByRole("dialog", { name: "Move site" });
  await dialog.getByRole("combobox", { name: "Application" }).selectOption(otherId);
  await dialog.getByRole("combobox", { name: "Stage" }).selectOption({ label: "New stage at the end" });
  await dialog.getByRole("textbox", { name: "New stage name" }).fill("Edge");
  await dialog.getByRole("button", { name: "Move component" }).click();

  await expect(page).toHaveURL(new RegExp(`/applications/${otherId}/workflow/nodes/${site.id}$`));
  await expect(page.getByText(/in stage 2, Edge/i)).toBeVisible();
  expect(moveBody).toEqual({ application_id: otherId, new_stage_name: "Edge" });
});
