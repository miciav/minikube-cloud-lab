import { test, expect } from "@playwright/test";
test("retained queue measurements use the selected window", async ({
  page,
}) => {
  await page.route("**/v1/**", async (route) => {
    const u = new URL(route.request().url());
    const data = u.pathname.endsWith("/history")
      ? {
          unit: "messages",
          start: 100,
          end: 200,
          sourceStatus: "available",
          series: [
            {
              resource: "000/jobs",
              points: [
                [120, 0],
                [190, 3],
              ],
            },
          ],
        }
      : [];
    await route.fulfill({ json: data });
  });
  await page.goto("/#/monitoring");
  await page
    .getByRole("combobox", { name: "Resources", exact: true })
    .selectOption("Queues");
  const changed = page.waitForRequest((r) =>
    r.url().includes("metric=visible&window=24h"),
  );
  await page
    .getByRole("combobox", { name: "History window" })
    .selectOption("24h");
  await changed;
  const region = page.getByRole("region", { name: "Available messages" });
  await expect(region).toContainText("3 messages");
  await expect(region.locator("svg circle")).toHaveCount(2);
});
