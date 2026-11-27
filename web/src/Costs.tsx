import { useState } from "react";
import { Badge, Field, Select } from "@fluentui/react-components";
import { Table } from "./sections";
import type { BillingReport } from "./types";
import { useResource } from "./useResource";

const eur = (v: number) => v.toFixed(4) + " EUR";

export function Costs({ refresh }: { refresh: number }) {
  const [since, setSince] = useState("");
  const { data, error } = useResource<BillingReport>(
    since ? "billing?since=" + since : "billing",
    refresh,
  );
  return (
    <section className="panel">
      <Field label="Period">
        <Select value={since} onChange={(_, d) => setSince(d.value)}>
          <option value="">Since reset</option>
          <option value="1h">Last hour</option>
          <option value="30m">Last 30 minutes</option>
        </Select>
      </Field>
      {error && <p role="alert">{error}</p>}
      {!data && !error && <p>Loading costs…</p>}
      {data && (
        <>
          <h2>{eur(data.total)}</h2>
          <p>Since reset at {new Date(data.resetAt).toLocaleString()}</p>
          {data.items.length ? (
            <Table
              columns={["Kind", "Name", "Type/Size", "Status", "Hours", "Cost"]}
            >
              {data.items.map((i) => (
                <tr key={i.kind + "/" + i.name}>
                  <td>{i.kind}</td>
                  <td>{i.name}</td>
                  <td>
                    {i.type ?? ((i.bytes ?? 0) / 2 ** 30).toFixed(1) + "Gi"}
                  </td>
                  <td>
                    <Badge
                      appearance="tint"
                      color={i.running ? "success" : "informative"}
                    >
                      {i.running ? "Running" : "Stopped"}
                    </Badge>
                  </td>
                  <td>{(i.seconds / 3600).toFixed(2)}</td>
                  <td>{eur(i.cost)}</td>
                </tr>
              ))}
            </Table>
          ) : (
            <p className="empty">No billable resources in this period.</p>
          )}
        </>
      )}
    </section>
  );
}
