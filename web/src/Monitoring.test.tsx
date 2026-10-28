import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { Monitoring } from "./Monitoring";
function snapshot() {
  return {
    observedAt: new Date().toISOString(),
    clusterState: "RUNNING",
    metricsStatus: "available",
    metricsReason: "",
    coveredNodes: 1,
    usage: { cpuMillicores: 250, memoryBytes: 134217728 },
    pods: { total: 6, pending: 1, unhealthy: 2 },
    nodes: [
      {
        name: "node-one",
        ready: true,
        requested: { cpuMillicores: 500, memoryBytes: 268435456 },
        allocatable: { cpuMillicores: 2000, memoryBytes: 2147483648 },
        usage: {
          cpuMillicores: 250,
          memoryBytes: 134217728,
          timestamp: new Date().toISOString(),
          window: "15s",
        },
      },
    ],
  };
}
const storage = { total: "20Gi", allocated: "1Gi", available: "19Gi" };
it("renders actual usage separately from requests", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(JSON.stringify(snapshot()))),
  );
  render(<Monitoring cluster="demo" storage={storage} />);
  expect(await screen.findByText("node-one")).toBeTruthy();
  expect(screen.getAllByText("0.25 cores")).toHaveLength(2);
  expect(screen.getAllByText("128 MiB")).toHaveLength(2);
  expect(screen.getByText("0.50 / 2.00 cores")).toBeTruthy();
  expect(screen.getByText("256 MiB / 2 GiB")).toBeTruthy();
  expect(screen.getByText("Allocated capacity")).toBeTruthy();
  expect(screen.getByText(/not bytes currently written/)).toBeTruthy();
});
it("renders unavailable partial stale and zero distinctly", async () => {
  let current: any = snapshot();
  current.metricsStatus = "unavailable";
  current.metricsReason = "Metrics API unavailable";
  current.usage = null;
  current.nodes[0].usage = null;
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementation(async () => new Response(JSON.stringify(current))),
  );
  const view = render(
    <Monitoring key="missing" cluster="demo" storage={null} />,
  );
  expect(await screen.findByText(/Metrics API unavailable/)).toBeTruthy();
  expect(screen.getAllByText("Unavailable").length).toBeGreaterThan(0);
  expect(screen.queryByText("0.00 cores")).toBeNull();
  current = { ...snapshot(), metricsStatus: "partial", usage: null };
  current.nodes.push({ ...current.nodes[0], name: "node-two", usage: null });
  view.rerender(<Monitoring key="partial" cluster="demo" storage={null} />);
  expect(await screen.findByText(/1 of 2 nodes covered/)).toBeTruthy();
  expect(screen.getAllByText("0.25 cores")).toHaveLength(1);
  current = snapshot();
  current.usage.cpuMillicores = 0;
  current.usage.memoryBytes = 0;
  current.nodes[0].usage.cpuMillicores = 0;
  current.nodes[0].usage.memoryBytes = 0;
  current.observedAt = new Date(Date.now() - 120000).toISOString();
  view.rerender(<Monitoring key="zero" cluster="demo" storage={null} />);
  expect((await screen.findAllByText("0.00 cores")).length).toBe(2);
  expect(screen.getByText(/Stale snapshot/)).toBeTruthy();
});
it("shows a recoverable cluster error instead of invented measurements", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "cluster data unavailable" }), {
        status: 503,
      }),
    ),
  );
  render(<Monitoring cluster="demo" storage={storage} />);
  expect(await screen.findByRole("alert")).toHaveProperty(
    "textContent",
    "cluster data unavailable",
  );
  expect(screen.queryByText("0.00 cores")).toBeNull();
});
it("ages a previously valid snapshot despite repeated identical failures", async () => {
  const { act } = await import("@testing-library/react");
  vi.useFakeTimers();
  try {
    const data = snapshot();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(data)))
      .mockRejectedValue(Error("cluster disconnected"));
    vi.stubGlobal("fetch", fetch);
    render(<Monitoring cluster="demo" storage={storage} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(screen.getByText("Live metrics")).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16000);
    });
    expect(screen.queryByText("Live metrics")).toBeNull();
    expect(screen.getByText("Previous metrics")).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(105000);
    });
    expect(screen.getByText(/Stale snapshot/)).toBeTruthy();
    expect(screen.getByText("Stale metrics")).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});
it("does not call old node observations live even when the snapshot envelope is new", async () => {
  const data = snapshot();
  data.nodes[0].usage.timestamp = new Date(Date.now() - 90000).toISOString();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(JSON.stringify(data))),
  );
  render(<Monitoring cluster="demo" storage={storage} />);
  expect(await screen.findByText("node-one")).toBeTruthy();
  expect(screen.queryByText("Live metrics")).toBeNull();
  expect(screen.getByText("Stale metrics")).toBeTruthy();
});
