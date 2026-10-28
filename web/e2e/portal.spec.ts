import { test, expect, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import type { Cluster, Volume, Operation } from "../src/types";
async function fixture(page: Page) {
  const state = {
    clusters: [] as Cluster[],
    volumes: [] as Volume[],
    operations: [] as Operation[],
    failClusters: false,
    failVolume: false,
  };
  await page.route("**/v1/**", async (route) => {
    const r = route.request(),
      path = new URL(r.url()).pathname,
      method = r.method();
    let data: unknown = {};
    let status = 200;
    if (path === "/v1/clusters") {
      if (method === "POST") {
        const body = r.postDataJSON();
        state.clusters = [
          {
            name: body.name,
            machineType: body.machineType,
            minMachines: body.minNodes,
            maxMachines: body.maxNodes,
            status: "RUNNING",
            createdAt: new Date().toISOString(),
          },
        ];
        state.operations = [
          {
            id: "op-1",
            type: "CREATE_CLUSTER",
            target: body.name,
            status: "DONE",
          },
        ];
        data = { id: "op-1" };
        status = 202;
      } else if (state.failClusters) {
        status = 503;
        data = { error: "cloud unavailable" };
      } else data = state.clusters;
    } else if (path === "/v1/machine-types")
      data = [{ name: "lab-small", cpus: 2, memoryMiB: 2048 }];
    else if (path === "/v1/machines") data = [];
    else if (path === "/v1/operations") data = state.operations;
    else if (path === "/v1/storage")
      data = { total: "20Gi", allocated: "1Gi", available: "19Gi" };
    else if (path === "/v1/volumes") {
      if (method === "POST") {
        if (state.failVolume) {
          status = 409;
          data = { error: "storage capacity exhausted" };
        } else {
          const body = r.postDataJSON();
          const v = {
            id: "pvc-browser",
            capacity: body.capacity,
            status: "Available",
            cluster: "",
            claim: "",
            policy: "Retain",
            enforced: true,
          };
          state.volumes.push(v);
          data = v;
          status = 201;
        }
      } else data = state.volumes;
    } else if (path === "/v1/volumes/pvc-browser/publish") {
      state.volumes[0].cluster = r.postDataJSON().cluster;
      state.volumes[0].status = "Published";
    } else if (path === "/v1/volumes/pvc-browser/unpublish") {
      state.volumes[0].cluster = "";
      state.volumes[0].status = "Available";
    } else if (path === "/v1/volumes/pvc-browser" && method === "DELETE") {
      state.volumes = [];
    } else if (path.endsWith("/kubeconfig")) {
      await route.fulfill({
        status: 200,
        contentType: "application/yaml",
        body: "apiVersion: v1\nkind: Config\n",
      });
      return;
    } else if (path.endsWith("/dashboard")) data = { port: 32123 };
    else if (path.endsWith("/monitoring"))
      data = {
        observedAt: new Date().toISOString(),
        clusterState: "RUNNING",
        nodes: [
          {
            name: "node-one",
            ready: true,
            allocatable: { cpuMillicores: 2000, memoryBytes: 2147483648 },
            requested: { cpuMillicores: 500, memoryBytes: 268435456 },
            usage: {
              cpuMillicores: 250,
              memoryBytes: 134217728,
              timestamp: new Date().toISOString(),
              window: "15s",
            },
          },
        ],
        pods: { total: 5, pending: 1, unhealthy: 0 },
        usage: { cpuMillicores: 250, memoryBytes: 134217728 },
        coveredNodes: 1,
        metricsStatus: "available",
        metricsReason: "",
      };
    else {
      status = 404;
      data = { error: "not found" };
    }
    await route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });
  return state;
}
async function createCluster(page: Page) {
  await page.getByRole("button", { name: "Create cluster" }).click();
  const d = page.getByRole("dialog");
  await d.getByLabel("Cluster name").fill("demo");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Operation op-1 accepted." }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Clusters", exact: true }).click();
  await page.getByRole("link", { name: "demo", exact: true }).click();
}

test("cluster creation, operation, monitoring, dashboard and credentials", async ({
  page,
}) => {
  await fixture(page);
  await page.route("http://127.0.0.1:32123/**", (r) =>
    r.fulfill({
      contentType: "text/html",
      body: '<html><body><h1>Dashboard fixture</h1><a href="/nodes">Nodes</a></body></html>',
    }),
  );
  await page.goto("/");
  await createCluster(page);
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download kubeconfig" }).click();
  const file = await download;
  expect(await readFile((await file.path())!, "utf8")).toContain(
    "kind: Config",
  );
  expect(
    await page.evaluate(() => localStorage.length + sessionStorage.length),
  ).toBe(0);
  await page.getByRole("tab", { name: "Monitoring", exact: true }).click();
  await expect(page.getByText("node-one")).toBeVisible();
  await expect(page.getByText("0.25 cores")).toHaveCount(2);
  await page.getByRole("tab", { name: "Kubernetes", exact: true }).click();
  await expect(
    page
      .frameLocator("iframe")
      .getByRole("heading", { name: "Dashboard fixture" }),
  ).toBeVisible();
  await page
    .frameLocator("iframe")
    .getByRole("link", { name: "Nodes" })
    .click();
  await expect(page.locator("iframe")).toHaveAttribute(
    "src",
    "http://127.0.0.1:32123/",
  );
  await page.getByRole("link", { name: "Operations", exact: true }).click();
  await expect(page.getByText("CREATE_CLUSTER")).toBeVisible();
  await expect(
    page.getByRole("button", { name: /autoscaler|load generator/i }),
  ).toHaveCount(0);
});

test("volume create publish unpublish and confirmed delete", async ({
  page,
}) => {
  const state = await fixture(page);
  state.clusters = [
    {
      name: "demo",
      machineType: "lab-small",
      minMachines: 1,
      maxMachines: 3,
      status: "RUNNING",
      createdAt: new Date().toISOString(),
    },
  ];
  await page.goto("/#/volumes");
  await page.getByRole("button", { name: "Create volume" }).click();
  let d = page.getByRole("dialog");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("button", { name: "Publish", exact: true }).click();
  d = page.getByRole("dialog");
  await d.getByRole("button", { name: "Publish", exact: true }).click();
  await page.getByRole("button", { name: "Unpublish", exact: true }).click();
  d = page.getByRole("dialog");
  await d.getByRole("button", { name: "Unpublish", exact: true }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  d = page.getByRole("dialog");
  await expect(
    d.getByRole("button", { name: "Delete", exact: true }),
  ).toBeDisabled();
  await d.getByLabel("Confirm resource name").fill("pvc-browser");
  await d.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByRole("cell", { name: "pvc-browser" })).toHaveCount(0);
  expect(state.volumes).toHaveLength(0);
});

test("backend errors, keyboard dialog and narrow viewport", async ({
  page,
}) => {
  const state = await fixture(page);
  state.failClusters = true;
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await expect(page.getByRole("alert")).toContainText("cloud unavailable");
  state.failClusters = false;
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.getByRole("link", { name: "Volumes", exact: true }).click();
  const trigger = page.getByRole("button", { name: "Create volume" });
  await trigger.click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await trigger.click();
  state.failVolume = true;
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "storage capacity exhausted",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});

test("removes obsolete cluster view after external deletion", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.goto("/");
  await createCluster(page);
  await page.getByRole("tab", { name: "Monitoring", exact: true }).click();
  await expect(page.getByText("node-one")).toBeVisible();
  state.clusters = [];
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByText("Cluster unavailable or deleted.")).toBeVisible();
  await expect(page.getByText("node-one")).toHaveCount(0);
});
