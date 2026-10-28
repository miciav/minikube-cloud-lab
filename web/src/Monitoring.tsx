import { useEffect, useState } from "react";
import { Badge, Spinner, Title2 } from "@fluentui/react-components";
import { useResource } from "./useResource";
import type { StorageCapacity } from "./types";
type Resources = { cpuMillicores: number; memoryBytes: number };
type Usage = Resources & { timestamp: string; window: string };
type Snapshot = {
  observedAt: string;
  clusterState: string;
  nodes: {
    name: string;
    ready: boolean;
    allocatable: Resources;
    requested: Resources;
    usage: Usage | null;
  }[];
  pods: { total: number; pending: number; unhealthy: number };
  metricsStatus: "available" | "partial" | "unavailable";
  metricsReason: string;
  coveredNodes: number;
  usage: Resources | null;
};
const cpu = (n: number) => `${(n / 1000).toFixed(2)} cores`;
function memory(n: number) {
  return n >= 1073741824
    ? `${Number((n / 1073741824).toFixed(2))} GiB`
    : `${Number((n / 1048576).toFixed(1))} MiB`;
}
export function Monitoring({
  cluster,
  storage,
  refresh = 0,
}: {
  cluster: string;
  storage: StorageCapacity | null;
  refresh?: number;
}) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const update = () => setNow(Date.now());
    const timer = window.setInterval(update, 5000);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);
  const { data, error } = useResource<Snapshot>(
    `clusters/${encodeURIComponent(cluster)}/monitoring`,
    refresh,
  );
  if (!data)
    return error ? (
      <div className="error" role="alert">
        {error}
      </div>
    ) : (
      <Spinner label="Loading live monitoring…" size="small" />
    );
  const stale =
    now - Date.parse(data.observedAt) > 60000 ||
    data.nodes.some(
      (n) => n.usage && now - Date.parse(n.usage.timestamp) > 60000,
    );
  return (
    <>
      <div className="monitor-meta">
        <Badge
          appearance="tint"
          color={
            !stale && !error && data.metricsStatus === "available"
              ? "success"
              : "warning"
          }
        >
          {stale
            ? "Stale metrics"
            : error
              ? "Previous metrics"
              : data.metricsStatus === "available"
                ? "Live metrics"
                : data.metricsStatus === "partial"
                  ? "Partial metrics"
                  : "Metrics unavailable"}
        </Badge>
        <span>
          {stale ? "Stale snapshot · " : ""}Observed{" "}
          {new Date(data.observedAt).toLocaleTimeString()} · refresh every 15
          seconds
        </span>
      </div>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      {data.metricsStatus !== "available" && (
        <div className="notice">
          {data.metricsReason || "Metrics data incomplete or stale"} ·{" "}
          {data.coveredNodes} of {data.nodes.length} nodes covered
        </div>
      )}
      <div className="stat-grid">
        <section className="stat">
          <span>Ready nodes</span>
          <strong>
            {data.nodes.filter((n) => n.ready).length} / {data.nodes.length}
          </strong>
          <small>Observed Kubernetes health</small>
        </section>
        <section className="stat">
          <span>
            {stale || error ? "Last known CPU usage" : "Actual CPU usage"}
          </span>
          <strong>
            {data.usage ? cpu(data.usage.cpuMillicores) : "Unavailable"}
          </strong>
          <small>Measured across all nodes</small>
        </section>
        <section className="stat">
          <span>
            {stale || error ? "Last known memory usage" : "Actual memory usage"}
          </span>
          <strong>
            {data.usage ? memory(data.usage.memoryBytes) : "Unavailable"}
          </strong>
          <small>Measured across all nodes</small>
        </section>
      </div>
      <section className="panel">
        <Title2>Nodes</Title2>
        <p className="muted">
          Actual usage is measured. Requests reserve capacity for scheduling.
        </p>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  "Node",
                  "Status",
                  "Actual CPU",
                  "Actual memory",
                  "CPU requested / allocatable",
                  "Memory requested / allocatable",
                  "Measurement",
                ].map((c) => (
                  <th key={c} scope="col">
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.nodes.map((n) => (
                <tr key={n.name}>
                  <td>{n.name}</td>
                  <td>{n.ready ? "Ready" : "Not ready"}</td>
                  <td>
                    {n.usage ? cpu(n.usage.cpuMillicores) : "Unavailable"}
                  </td>
                  <td>
                    {n.usage ? memory(n.usage.memoryBytes) : "Unavailable"}
                  </td>
                  <td>
                    {(n.requested.cpuMillicores / 1000).toFixed(2)} /{" "}
                    {(n.allocatable.cpuMillicores / 1000).toFixed(2)} cores
                  </td>
                  <td>
                    {memory(n.requested.memoryBytes)} /{" "}
                    {memory(n.allocatable.memoryBytes)}
                  </td>
                  <td>
                    {n.usage
                      ? `${new Date(n.usage.timestamp).toLocaleTimeString()} · ${n.usage.window} window`
                      : "Unavailable"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="panel">
        <Title2>Workload health</Title2>
        <dl className="details">
          <div>
            <dt>Total pods</dt>
            <dd>{data.pods.total}</dd>
          </div>
          <div>
            <dt>Pending pods</dt>
            <dd>{data.pods.pending}</dd>
          </div>
          <div>
            <dt>Unhealthy pods</dt>
            <dd>{data.pods.unhealthy}</dd>
          </div>
        </dl>
        <p className="muted">
          Inspect individual workloads and logs in the Kubernetes dashboard.
        </p>
      </section>
      <section className="panel">
        <Title2>Cloud storage budget</Title2>
        {storage ? (
          <dl className="details">
            <div>
              <dt>Total capacity</dt>
              <dd>{storage.total}</dd>
            </div>
            <div>
              <dt>Allocated capacity</dt>
              <dd>{storage.allocated}</dd>
            </div>
            <div>
              <dt>Available capacity</dt>
              <dd>{storage.available}</dd>
            </div>
          </dl>
        ) : (
          <p>Storage budget unavailable.</p>
        )}
        <p className="muted">
          Allocated capacity is reserved by the cloud, not bytes currently
          written.
        </p>
      </section>
    </>
  );
}
