import { test, expect } from "@playwright/test";
test.skip(
  !process.env.PORTAL_REAL_URL,
  "Opt-in: use only a disposable cloud with isolated state and storage.",
);
test("real Docker portal creates a small cluster, monitors it and manages storage", async ({
  page,
}, info) => {
  test.setTimeout(12 * 60 * 1000);
  const name = `portal-ui-${Date.now()}`;
  const base = process.env.PORTAL_REAL_URL!;
  async function completed() {
    await expect
      .poll(
        async () => {
          const r = await page.request.get(`${base}/v1/operations`);
          const ops = await r.json();
          const op = ops
            .filter((o: { target: string }) => o.target === name)
            .at(-1);
          if (op?.status === "ERROR") throw Error(op.error);
          return op?.status;
        },
        { timeout: 6 * 60 * 1000, intervals: [1000, 2000] },
      )
      .toBe("DONE");
  }
  try {
    await page.goto(base);
    await page.getByRole("button", { name: "Create cluster" }).click();
    const d = page.getByRole("dialog");
    await d.getByLabel("Cluster name").fill(name);
    await d.getByRole("button", { name: "Create", exact: true }).click();
    await expect(
      page.getByRole("status").filter({ hasText: "accepted." }),
    ).toBeVisible();
    await completed();
    await page.getByRole("link", { name: "Clusters", exact: true }).click();
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByRole("link", { name, exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Download kubeconfig" }),
    ).toBeEnabled();
    await page.getByRole("tab", { name: "Monitoring", exact: true }).click();
    await expect(page.getByText("Live metrics", { exact: true })).toBeVisible({
      timeout: 3 * 60 * 1000,
    });
    await expect(page.getByRole("cell", { name, exact: true })).toBeVisible();
    await page.screenshot({
      path: info.outputPath("real-monitoring.png"),
      fullPage: true,
    });
    await page.getByRole("tab", { name: "Kubernetes", exact: true }).click();
    await expect(page.locator("iframe")).toBeVisible({
      timeout: 3 * 60 * 1000,
    });
    await expect(
      page
        .frameLocator("iframe")
        .getByText("Kubernetes Dashboard", { exact: true })
        .first(),
    ).toBeVisible({ timeout: 60000 });
    await page.screenshot({
      path: info.outputPath("real-dashboard.png"),
      fullPage: true,
    });
    await page.getByRole("link", { name: "Volumes", exact: true }).click();
    await page.getByRole("button", { name: "Create volume" }).click();
    await page.getByRole("dialog").getByLabel("Capacity").fill("32Mi");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Create", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const row = page
      .getByRole("row")
      .filter({
        has: page.getByRole("button", { name: "Publish", exact: true }),
      });
    await expect(row).toHaveCount(1);
    const id = (await row.getByRole("cell").first().innerText()).split("\n")[0];
    await row.getByRole("button", { name: "Publish", exact: true }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Publish", exact: true })
      .click();
    await page.getByRole("button", { name: "Unpublish", exact: true }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Unpublish", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await page.getByRole("dialog").getByLabel("Confirm resource name").fill(id);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Delete", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("cell", { name: id })).toHaveCount(0);
    await page.getByRole("link", { name: "Clusters", exact: true }).click();
    await page.getByRole("link", { name, exact: true }).click();
    await page
      .getByRole("button", { name: "Delete cluster", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByLabel("Confirm resource name")
      .fill(name);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Delete", exact: true })
      .click();
    await completed();
    console.log("REAL CLOUD/SMALL/METRICS/YAKD/STORAGE/DELETE PASS", name);
  } finally {
    const r = await page.request.get(`${base}/v1/clusters`);
    if (
      r.ok() &&
      (await r.json()).some((c: { name: string }) => c.name === name)
    ) {
      await completed();
      const deletion = await page.request.delete(`${base}/v1/clusters/${name}`);
      if (deletion.ok()) await completed();
    }
  }
});
