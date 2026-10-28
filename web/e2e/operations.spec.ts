import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { waitForOperation } from "./operations";
test("does not accept the old CREATE when the requested DELETE has failed", async ({
  request,
}) => {
  const failed = {
    id: "op-delete",
    target: "demo",
    status: "ERROR",
    error: "delete failed",
  };
  const old = { id: "op-create", target: "demo", status: "DONE" };
  const server = createServer((r, w) => {
    w.setHeader("Content-Type", "application/json");
    w.end(JSON.stringify(r.url === "/v1/operations" ? [failed, old] : failed));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  try {
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    let rejected = false;
    try {
      await waitForOperation(request, base, failed);
    } catch (e) {
      rejected = String(e).includes("delete failed");
    }
    expect(rejected, "must reject DELETE ERROR despite old CREATE DONE").toBe(
      true,
    );
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((e) => (e ? reject(e) : resolve())),
    );
  }
});
