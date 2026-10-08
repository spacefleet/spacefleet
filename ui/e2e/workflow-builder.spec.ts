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
  await expect(page).toHaveURL(/\/workflow\/nodes\/.+\?new=terraform&stage=/);
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

  // The card menu does the same without dragging.
  await deploy.getByRole("button", { name: "config actions" }).click();
  await page.getByRole("menuitem", { name: "Move to previous stage" }).click();
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
