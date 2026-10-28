import { Spinner } from "@fluentui/react-components";
import { useResource } from "./useResource";
export function Dashboard({
  cluster,
  refresh = 0,
}: {
  cluster: string;
  refresh?: number;
}) {
  const { data, error } = useResource<{ port: number }>(
    `clusters/${encodeURIComponent(cluster)}/dashboard`,
    refresh,
  );
  if (error)
    return (
      <div className="error" role="alert">
        {error}. The dashboard may still be starting; retrying every 15 seconds.
      </div>
    );
  if (!data)
    return <Spinner size="small" label="Waiting for Kubernetes dashboard…" />;
  if (!Number.isInteger(data.port) || data.port < 1 || data.port > 65535)
    return (
      <div className="error" role="alert">
        Dashboard endpoint unavailable.
      </div>
    );
  const url = `http://127.0.0.1:${data.port}/`;
  return (
    <section className="dashboard">
      <div className="toolbar">
        <a href={url} target="_blank" rel="noopener noreferrer">
          Open dashboard in a new tab
        </a>
        <span className="muted">YAKD · local cluster administration</span>
      </div>
      <iframe
        key={url}
        title="Kubernetes dashboard"
        src={url}
        referrerPolicy="no-referrer"
      />
    </section>
  );
}
