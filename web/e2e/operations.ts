import { expect, type APIRequestContext } from "@playwright/test";
import type { Operation } from "../src/types";
export async function waitForOperation(
  request: APIRequestContext,
  base: string,
  operation: Pick<Operation, "id">,
) {
  await expect
    .poll(
      async () => {
        const r = await request.get(
          `${base}/v1/operations/${encodeURIComponent(operation.id)}`,
        );
        expect(r.ok()).toBe(true);
        const op: Operation = await r.json();
        if (op.status === "ERROR") throw Error(op.error);
        return op.status;
      },
      { timeout: 6 * 60 * 1000, intervals: [1000, 2000] },
    )
    .toBe("DONE");
}
