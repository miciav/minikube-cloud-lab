import { render, screen } from "@testing-library/react";
import { it, expect, vi } from "vitest";
import { App } from "./App";
it("shows cloud overview and recoverable API error", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(Error("Cloud unavailable")));
  render(<App />);
  expect(screen.getByText("Cloud Lab")).toBeTruthy();
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("Install autoscaler")).toBeNull();
});

import { beforeEach } from "vitest";
import { within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
const cluster = {
  name: "demo",
  machineType: "lab-small",
  minMachines: 1,
  maxMachines: 3,
  status: "RUNNING",
  createdAt: "2026-10-28T00:00:00Z",
};
const volume = {
  id: "pvc-test",
  capacity: "1Gi",
  status: "Available",
  cluster: "",
  claim: "",
  policy: "Retain",
  enforced: true,
};
function fixture(
  handler?: (path: string, init: RequestInit) => Response | undefined,
) {
  return vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const path = String(input);
    const override = handler?.(path, init);
    if (override) return override;
    const data: Record<string, unknown> = {
      "/v1/clusters": [cluster],
      "/v1/machine-types": [{ name: "lab-small", cpus: 2, memoryMiB: 2048 }],
      "/v1/machines": [],
      "/v1/volumes": [volume],
      "/v1/operations": [],
      "/v1/storage": { total: "20Gi", allocated: "1Gi", available: "19Gi" },
    };
    return new Response(JSON.stringify(data[path] ?? {}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
}
beforeEach(() => {
  window.history.replaceState(null, "", "#/");
});
it("creates cluster and tracks operation failure", async () => {
  const u = userEvent.setup();
  const fetch = fixture((path, init) => {
    if (path === "/v1/clusters" && init.method === "POST")
      return new Response(JSON.stringify({ id: "op-1" }), { status: 202 });
    if (path === "/v1/operations")
      return new Response(
        JSON.stringify([
          {
            id: "op-1",
            target: "new-cluster",
            type: "CREATE_CLUSTER",
            status: "ERROR",
            error: "bootstrap failed",
          },
        ]),
      );
  });
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(await screen.findByRole("button", { name: "Create cluster" }));
  const d = screen.getByRole("dialog");
  await u.type(within(d).getByLabelText("Cluster name"), "new-cluster");
  await u.click(within(d).getByRole("button", { name: "Create" }));
  expect(await screen.findByText(/op-1.*accepted/)).toBeTruthy();
  expect(fetch).toHaveBeenCalledWith(
    "/v1/clusters",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        name: "new-cluster",
        machineType: "lab-small",
        minNodes: 1,
        maxNodes: 3,
      }),
    }),
  );
  await u.click(screen.getByRole("link", { name: "Operations" }));
  expect(await screen.findByText("bootstrap failed")).toBeTruthy();
});
it("confirms destructive actions and preserves conflict errors", async () => {
  const u = userEvent.setup();
  const fetch = fixture((path, init) =>
    path === "/v1/volumes/pvc-test" && init.method === "DELETE"
      ? new Response(JSON.stringify({ error: "volume is still in use" }), {
          status: 412,
        })
      : undefined,
  );
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Volumes" }));
  const row = (await screen.findByText("pvc-test")).closest("tr")!;
  await u.click(within(row).getByRole("button", { name: "Delete" }));
  const dialog = screen.getByRole("dialog");
  expect(
    (
      within(dialog).getByRole("button", {
        name: "Delete",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  await u.type(
    within(dialog).getByLabelText("Confirm resource name"),
    "pvc-test",
  );
  await u.click(within(dialog).getByRole("button", { name: "Delete" }));
  expect(await within(dialog).findByRole("alert")).toHaveProperty(
    "textContent",
    "volume is still in use",
  );
  expect(
    fetch.mock.calls.filter(
      ([path, opts]) =>
        path === "/v1/volumes/pvc-test" && opts?.method === "DELETE",
    ),
  ).toHaveLength(1);
});
it("publishes and unpublishes volumes", async () => {
  const u = userEvent.setup();
  let published = false;
  const fetch = fixture((path, init) => {
    if (path === "/v1/volumes/pvc-test/publish") {
      published = true;
      return new Response("{}");
    }
    if (path === "/v1/volumes/pvc-test/unpublish") {
      published = false;
      return new Response("{}");
    }
    if (path === "/v1/volumes")
      return new Response(
        JSON.stringify([
          {
            ...volume,
            cluster: published ? "demo" : "",
            status: published ? "Published" : "Available",
          },
        ]),
      );
  });
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Volumes" }));
  await u.click(await screen.findByRole("button", { name: "Publish" }));
  let d = screen.getByRole("dialog");
  await u.click(within(d).getByRole("button", { name: "Publish" }));
  await u.click(await screen.findByRole("button", { name: "Unpublish" }));
  d = screen.getByRole("dialog");
  await u.click(within(d).getByRole("button", { name: "Unpublish" }));
  expect(fetch).toHaveBeenCalledWith(
    "/v1/volumes/pvc-test/publish",
    expect.objectContaining({ body: JSON.stringify({ cluster: "demo" }) }),
  );
  expect(fetch).toHaveBeenCalledWith(
    "/v1/volumes/pvc-test/unpublish",
    expect.objectContaining({ method: "POST" }),
  );
});
it("downloads kubeconfig without storing credentials", async () => {
  const { downloadKubeconfig } = await import("./api");
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response("SECRET-TEST-CONFIG", {
        headers: { "Content-Type": "application/yaml" },
      }),
    ),
  );
  const create = vi.fn().mockReturnValue("blob:test");
  const revoke = vi.fn();
  vi.stubGlobal(
    "URL",
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke }),
  );
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  const stored = vi.spyOn(Storage.prototype, "setItem");
  await downloadKubeconfig("demo");
  expect(create).toHaveBeenCalledTimes(1);
  expect(revoke).toHaveBeenCalledWith("blob:test");
  expect(stored).not.toHaveBeenCalled();
});
it("prevents double submission and preserves capacity form on failure", async () => {
  const u = userEvent.setup();
  let resolve!: (v: Response) => void;
  const fetch = fixture();
  fetch.mockImplementation(async (input, init = {}) => {
    if (String(input) === "/v1/volumes" && init.method === "POST")
      return new Promise<Response>((r) => {
        resolve = r;
      });
    const data: Record<string, unknown> = {
      "/v1/clusters": [cluster],
      "/v1/machine-types": [{ name: "lab-small", cpus: 2, memoryMiB: 2048 }],
      "/v1/machines": [],
      "/v1/volumes": [volume],
      "/v1/operations": [],
      "/v1/storage": { total: "20Gi", allocated: "1Gi", available: "19Gi" },
    };
    return new Response(JSON.stringify(data[String(input)] ?? {}));
  });
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Volumes" }));
  await u.click(await screen.findByRole("button", { name: "Create volume" }));
  const d = screen.getByRole("dialog");
  const capacity = within(d).getByLabelText("Capacity");
  await u.clear(capacity);
  await u.type(capacity, "5Gi");
  await u.dblClick(within(d).getByRole("button", { name: "Create" }));
  expect(
    fetch.mock.calls.filter(
      ([p, i]) => p === "/v1/volumes" && i?.method === "POST",
    ),
  ).toHaveLength(1);
  await import("@testing-library/react").then(({ act }) =>
    act(async () =>
      resolve(
        new Response(JSON.stringify({ error: "storage capacity exhausted" }), {
          status: 409,
        }),
      ),
    ),
  );
  expect(await within(d).findByRole("alert")).toHaveProperty(
    "textContent",
    "storage capacity exhausted",
  );
  expect(capacity).toHaveProperty("value", "5Gi");
});

it("invalidates a destructive confirmation when the selected cluster is recreated", async () => {
  const { act, waitFor } = await import("@testing-library/react");
  const u = userEvent.setup();
  let generation = cluster.createdAt;
  const fetch = fixture((path, init) =>
    path === "/v1/clusters" && init.method !== "POST"
      ? new Response(JSON.stringify([{ ...cluster, createdAt: generation }]))
      : undefined,
  );
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Clusters" }));
  await u.click(await screen.findByRole("button", { name: "Delete" }));
  const d = screen.getByRole("dialog");
  await u.type(within(d).getByLabelText("Confirm resource name"), "demo");
  generation = "2026-10-28T01:00:00Z";
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(
    fetch.mock.calls.filter(
      ([p, i]) => p === "/v1/clusters/demo" && i?.method === "DELETE",
    ),
  ).toHaveLength(0);
});
it("sends the confirmed generation when deleting a cluster and publishing a volume", async () => {
  const u = userEvent.setup();
  const fetch = fixture((p, i) =>
    p === "/v1/clusters/demo" && i.method === "DELETE"
      ? new Response(JSON.stringify({ id: "op-delete" }), { status: 202 })
      : undefined,
  );
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Clusters" }));
  await u.click(await screen.findByRole("button", { name: "Delete" }));
  const d = screen.getByRole("dialog");
  await u.type(within(d).getByLabelText("Confirm resource name"), "demo");
  await u.click(within(d).getByRole("button", { name: "Delete" }));
  expect(fetch).toHaveBeenCalledWith(
    "/v1/clusters/demo",
    expect.objectContaining({
      headers: expect.objectContaining({
        "If-Match": JSON.stringify(cluster.createdAt),
      }),
    }),
  );
  await u.click(screen.getByRole("link", { name: "Volumes" }));
  await u.click(await screen.findByRole("button", { name: "Publish" }));
  await u.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Publish" }),
  );
  expect(fetch).toHaveBeenCalledWith(
    "/v1/volumes/pvc-test/publish",
    expect.objectContaining({
      headers: expect.objectContaining({
        "If-Match": JSON.stringify(cluster.createdAt),
      }),
    }),
  );
});
it("invalidates publication when its selected cluster is replaced", async () => {
  const { act, waitFor } = await import("@testing-library/react");
  const u = userEvent.setup();
  let generation = cluster.createdAt;
  const fetch = fixture((p, i) =>
    p === "/v1/clusters" && i.method !== "POST"
      ? new Response(JSON.stringify([{ ...cluster, createdAt: generation }]))
      : undefined,
  );
  vi.stubGlobal("fetch", fetch);
  render(<App />);
  await u.click(screen.getByRole("link", { name: "Volumes" }));
  await u.click(await screen.findByRole("button", { name: "Publish" }));
  generation = "2026-10-28T02:00:00Z";
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(
    fetch.mock.calls.filter(
      ([p, i]) => String(p).endsWith("/publish") && i?.method === "POST",
    ),
  ).toHaveLength(0);
});
