import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { Costs } from "./Costs";

const report = {
  resetAt: "2026-11-27T08:00:00Z",
  since: "2026-11-27T08:00:00Z",
  until: "2026-11-27T09:00:00Z",
  currency: "EUR",
  total: 0.4,
  items: [
    {
      kind: "machine",
      name: "demo",
      cluster: "demo",
      type: "lab-small",
      running: true,
      seconds: 3600,
      cost: 0.1,
    },
    {
      kind: "volume",
      name: "pvc-1",
      bytes: 2147483648,
      running: false,
      seconds: 1800,
      cost: 0.3,
    },
  ],
};

it("shows the total, statuses and requests each period", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(report)));
  vi.stubGlobal("fetch", fetch);
  render(<Costs refresh={0} />);
  expect(await screen.findByText("0.4000 EUR")).toBeTruthy();
  expect(screen.getByText(/Since reset at/)).toBeTruthy();
  const demo = screen.getByRole("cell", { name: "demo" }).closest("tr")!;
  expect(within(demo).getByText("Running")).toBeTruthy();
  expect(within(demo).getByText("1.00")).toBeTruthy();
  const pvc = screen.getByRole("cell", { name: "pvc-1" }).closest("tr")!;
  expect(within(pvc).getByText("Stopped")).toBeTruthy();
  expect(within(pvc).getByText("2.0Gi")).toBeTruthy();
  expect(within(pvc).getByText("0.3000 EUR")).toBeTruthy();
  expect(fetch).toHaveBeenLastCalledWith("/v1/billing", expect.anything());
  const period = screen.getByLabelText("Period");
  fireEvent.change(period, { target: { value: "1h" } });
  expect(fetch).toHaveBeenLastCalledWith(
    "/v1/billing?since=1h",
    expect.anything(),
  );
  fireEvent.change(period, { target: { value: "30m" } });
  expect(fetch).toHaveBeenLastCalledWith(
    "/v1/billing?since=30m",
    expect.anything(),
  );
  fireEvent.change(period, { target: { value: "" } });
  expect(fetch).toHaveBeenLastCalledWith("/v1/billing", expect.anything());
});

it("reports an empty period and errors", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify({ ...report, items: [] }))),
  );
  const { unmount } = render(<Costs refresh={0} />);
  expect(
    await screen.findByText("No billable resources in this period."),
  ).toBeTruthy();
  unmount();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ error: "billing unavailable" }), {
          status: 503,
        }),
    ),
  );
  render(<Costs refresh={0} />);
  expect(await screen.findByText("billing unavailable")).toBeTruthy();
});
