import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { History, plotSegments } from "./History";
it("preserves a missing scrape and a measured zero", () => {
  expect(
    plotSegments(
      [
        [100, 0],
        [115, 2],
        [160, 1],
      ],
      100,
      175,
      2,
    ),
  ).toHaveLength(2);
});
it("reports a disabled backend with its recovery command", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: "Monitoring is unavailable. Check lab status monitoring.",
        }),
        { status: 503 },
      ),
    ),
  );
  render(<History refresh={0} />);
  expect(
    (await screen.findAllByText(/lab status monitoring/)).length,
  ).toBeGreaterThan(0);
});

it("labels a retained sample after its source goes away", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const data = path.endsWith("/ready")
        ? { ready: true }
        : {
            start: 100,
            end: 900,
            unit: "objects",
            sourceStatus: "unavailable",
            series: [{ resource: "course-files", points: [[120, 0]] }],
          };
      return new Response(JSON.stringify(data));
    }),
  );
  render(<History refresh={0} />);
  expect((await screen.findAllByText(/Last observed/)).length).toBeGreaterThan(
    0,
  );
  expect(
    screen.getAllByText(/waiting for a fresh sample/).length,
  ).toBeGreaterThan(0);
});
