import { useState } from "react";
import { useResource } from "./useResource";

type Point = [number, number];
type Series = { resource: string; points: Point[] };
type HistoryData = {
  unit: string;
  start: number;
  end: number;
  sourceStatus: string;
  series: Series[];
};
const metrics = [
  ["cpu", "Cluster CPU", "Kubernetes"],
  ["memory", "Cluster memory", "Kubernetes"],
  ["visible", "Available messages", "Queues"],
  ["inflight", "In-flight messages", "Queues"],
  ["delayed", "Delayed messages", "Queues"],
  ["objects", "Bucket objects", "Buckets"],
  ["bytes", "Bucket size", "Buckets"],
];
const format = new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 });
const time = (seconds: number) => new Date(seconds * 1000).toLocaleTimeString();

export function plotSegments(
  points: Point[],
  start: number,
  end: number,
  max: number,
) {
  const segments: string[][] = [];
  let previous = -Infinity;
  for (const [timestamp, value] of points) {
    if (timestamp - previous > 25) segments.push([]);
    const x = ((timestamp - start) / (end - start)) * 600;
    const y = 68 - (value / Math.max(max, 0.001)) * 64;
    segments[segments.length - 1].push(x.toFixed(1) + "," + y.toFixed(1));
    previous = timestamp;
  }
  return segments.map((s) => s.join(" "));
}

function Measure({
  metric,
  label,
  windowSize,
  refresh,
}: {
  metric: string;
  label: string;
  windowSize: string;
  refresh: number;
}) {
  const { data, error } = useResource<HistoryData>(
    "monitoring/history?metric=" + metric + "&window=" + windowSize,
    refresh,
  );
  return (
    <section className="history-metric" aria-label={label}>
      <h2>{label}</h2>
      {error && <p role="status">{error}</p>}
      {!data && !error && <p>Loading samples…</p>}
      {data && (
        <>
          {data.sourceStatus !== "available" && (
            <p>Source unavailable or waiting for a fresh sample.</p>
          )}
          {!data.series.length && <p>No samples in this window.</p>}
          {data.series.map((s) => {
            const last = s.points[s.points.length - 1];
            const max = Math.max(...s.points.map((p) => p[1]));
            return (
              <figure key={s.resource}>
                <figcaption>
                  <strong>{s.resource}</strong> · {format.format(last[1])}{" "}
                  {data.unit} ·{" "}
                  {data.end - last[0] > 45 || error
                    ? "Last observed"
                    : "Observed"}{" "}
                  {time(last[0])}
                </figcaption>
                <div className="history-scale">
                  {format.format(max)} {data.unit}
                </div>
                <svg
                  viewBox="0 0 600 72"
                  role="img"
                  aria-label={
                    label +
                    " for " +
                    s.resource +
                    " from " +
                    time(data.start) +
                    " to " +
                    time(data.end)
                  }
                >
                  {plotSegments(s.points, data.start, data.end, max).map(
                    (segment, i) =>
                      segment.includes(" ") ? (
                        <polyline
                          key={i}
                          points={segment}
                          fill="none"
                          stroke="currentColor"
                          strokeWidth="2"
                        />
                      ) : (
                        <circle
                          key={i}
                          cx={segment.split(",")[0]}
                          cy={segment.split(",")[1]}
                          r="2"
                          fill="currentColor"
                        />
                      ),
                  )}
                </svg>
                <div className="history-axis">
                  <span>{time(data.start)}</span>
                  <span>{time(data.end)}</span>
                </div>
              </figure>
            );
          })}
        </>
      )}
    </section>
  );
}

export function History({ refresh }: { refresh: number }) {
  const [windowSize, setWindow] = useState("15m");
  const [source, setSource] = useState("All resources");
  const ready = useResource<{ ready: boolean }>("monitoring/ready", refresh);
  if (ready.error) return <p role="status">{ready.error}</p>;
  if (!ready.data) return <p>Checking monitoring…</p>;
  const sources = ["All resources", "Kubernetes", "Queues", "Buckets"];
  return (
    <>
      <div className="history-filters">
        <label>
          Resources{" "}
          <select value={source} onChange={(e) => setSource(e.target.value)}>
            {sources.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </label>
        <label>
          History window{" "}
          <select
            value={windowSize}
            onChange={(e) => setWindow(e.target.value)}
          >
            <option value="15m">15 minutes</option>
            <option value="1h">1 hour</option>
            <option value="24h">24 hours</option>
          </select>
        </label>
      </div>
      <div className="history-grid">
        {metrics.map(
          ([metric, label, group]) =>
            (source === sources[0] || source === group) && (
              <Measure
                key={metric}
                metric={metric}
                label={label}
                windowSize={windowSize}
                refresh={refresh}
              />
            ),
        )}
      </div>
    </>
  );
}
