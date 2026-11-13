import type { ReactNode } from "react";
import {
  Badge,
  Button,
  Title2,
  useRestoreFocusTarget,
} from "@fluentui/react-components";
import type {
  Cluster,
  Machine,
  MachineType,
  Operation,
  StorageCapacity,
  Volume,
} from "./types";

export type Action = {
  kind:
    | "createCluster"
    | "deleteCluster"
    | "createVolume"
    | "deleteVolume"
    | "publish"
    | "unpublish";
  target?: string;
  generation?: string;
};
type Open = (next: Action) => void;
type Loaded<T> = { data: T | null; error: string };

export function Table({
  columns,
  children,
}: {
  columns: string[];
  children: ReactNode;
}) {
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {columns.map((v) => (
              <th key={v} scope="col">
                {v}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}
export function Status({ value }: { value: string }) {
  return (
    <Badge
      appearance="tint"
      color={
        value === "RUNNING" || value === "DONE" || value === "Available"
          ? "success"
          : value === "ERROR"
            ? "danger"
            : "informative"
      }
    >
      {value}
    </Badge>
  );
}
export function Empty() {
  return <p className="empty">No resources yet.</p>;
}

export function OverviewSection({
  clusters,
  machines,
  storage,
  open,
}: {
  clusters: Cluster[];
  machines: Machine[];
  storage: StorageCapacity | null;
  open: Open;
}) {
  const restoreFocusTarget = useRestoreFocusTarget();
  return (
    <>
      <div className="stat-grid">
        <section className="stat">
          <span>Clusters</span>
          <strong>{clusters.length}</strong>
          <small>
            {clusters.filter((c) => c.status === "RUNNING").length}{" "}
            running
          </small>
        </section>
        <section className="stat">
          <span>Machines</span>
          <strong>{machines.length}</strong>
          <small>Control plane and workers</small>
        </section>
        <section className="stat">
          <span>Storage allocated</span>
          <strong>
            {storage?.allocated ?? "Unavailable"}
          </strong>
          <small>
            of {storage?.total ?? "—"} cloud budget
          </small>
        </section>
      </div>
      <section className="panel">
        <Title2>Get started</Title2>
        <p>
          Create a Kubernetes cluster, manage persistent volumes, and
          inspect cluster health.
        </p>
        <Button
          {...restoreFocusTarget}
          appearance="primary"
          onClick={() => open({ kind: "createCluster" })}
        >
          Create cluster
        </Button>{" "}
        <a href="#/volumes">Manage storage</a>
      </section>
      <section className="panel">
        <Title2>Your clusters</Title2>
        {clusters.length === 0 ? (
          <Empty />
        ) : (
          <ul className="resource-list">
            {clusters.map((c) => (
              <li key={c.name}>
                <a href={`#/clusters/${c.name}`}>{c.name}</a>
                <Status value={c.status} />
                <span>{c.machineType}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

export function ClusterList({ clusters, open }: { clusters: Cluster[]; open: Open }) {
  const restoreFocusTarget = useRestoreFocusTarget();
  return (
    <>
      <div className="toolbar">
        <Button
          {...restoreFocusTarget}
          appearance="primary"
          onClick={() => open({ kind: "createCluster" })}
        >
          Create cluster
        </Button>
        <span>{clusters.length} resources</span>
      </div>
      <section className="panel">
        {clusters.length === 0 ? (
          <Empty />
        ) : (
          <Table
            columns={[
              "Name",
              "Status",
              "Machine type",
              "Node range",
              "Actions",
            ]}
          >
            {clusters.map((c) => (
              <tr key={c.name}>
                <td>
                  <a href={`#/clusters/${c.name}`}>{c.name}</a>
                </td>
                <td>
                  <Status value={c.status} />
                </td>
                <td>{c.machineType}</td>
                <td>
                  {c.minMachines}–{c.maxMachines}
                </td>
                <td>
                  <Button
                    {...restoreFocusTarget}
                    onClick={() =>
                      open({ kind: "deleteCluster", target: c.name })
                    }
                  >
                    Delete
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </section>
    </>
  );
}

export function MachinesSection({
  types,
  machines,
}: {
  types: MachineType[];
  machines: Machine[];
}) {
  return (
    <>
      <section className="panel">
        <Title2>Machine catalog</Title2>
        <Table columns={["Type", "CPUs", "Memory"]}>
          {types.map((v) => (
            <tr key={v.name}>
              <td>{v.name}</td>
              <td>{v.cpus}</td>
              <td>{v.memoryMiB} MiB</td>
            </tr>
          ))}
        </Table>
      </section>
      <section className="panel">
        <Title2>Allocated machines</Title2>
        {machines.length === 0 ? (
          <Empty />
        ) : (
          <Table
            columns={[
              "Name",
              "Cluster",
              "Role",
              "Status",
              "Created by",
            ]}
          >
            {machines.map((m) => (
              <tr key={m.name}>
                <td>{m.name}</td>
                <td>{m.cluster}</td>
                <td>{m.controlPlane ? "Control plane" : "Worker"}</td>
                <td>
                  <Status value={m.status} />
                </td>
                <td>{m.createdBy}</td>
              </tr>
            ))}
          </Table>
        )}
      </section>
    </>
  );
}

export function VolumesSection({
  volumes,
  storage,
  open,
}: {
  volumes: Volume[];
  storage: Loaded<StorageCapacity>;
  open: Open;
}) {
  const restoreFocusTarget = useRestoreFocusTarget();
  return (
    <>
      <div className="toolbar">
        <Button
          {...restoreFocusTarget}
          appearance="primary"
          onClick={() => open({ kind: "createVolume" })}
        >
          Create volume
        </Button>
      </div>
      <section className="panel">
        <Title2>Cloud storage budget</Title2>
        {storage.data ? (
          <dl className="details">
            <div>
              <dt>Total</dt>
              <dd>{storage.data.total}</dd>
            </div>
            <div>
              <dt>Allocated capacity</dt>
              <dd>{storage.data.allocated}</dd>
            </div>
            <div>
              <dt>Available</dt>
              <dd>{storage.data.available}</dd>
            </div>
          </dl>
        ) : (
          <p>{storage.error || "Loading storage budget…"}</p>
        )}
        <p className="muted">
          Allocated capacity is reserved storage, not bytes currently
          written.
        </p>
      </section>
      <section className="panel">
        {volumes.length === 0 ? (
          <Empty />
        ) : (
          <Table
            columns={[
              "Volume",
              "Capacity",
              "Status",
              "Cluster / claim",
              "Actions",
            ]}
          >
            {volumes.map((v) => (
              <tr key={v.id}>
                <td>
                  {v.id}
                  <small className="block">
                    {v.policy} ·{" "}
                    {v.enforced ? "Enforced capacity" : "No physical write quota"}
                  </small>
                </td>
                <td>{v.capacity}</td>
                <td>
                  <Status value={v.status} />
                </td>
                <td>
                  {v.cluster || "—"}
                  {v.claim && (
                    <small className="block">{v.claim}</small>
                  )}
                </td>
                <td>
                  <div className="row-actions">
                    {!v.cluster ? (
                      <Button
                        {...restoreFocusTarget}
                        onClick={() =>
                          open({ kind: "publish", target: v.id })
                        }
                      >
                        Publish
                      </Button>
                    ) : (
                      <Button
                        {...restoreFocusTarget}
                        onClick={() =>
                          open({ kind: "unpublish", target: v.id })
                        }
                      >
                        Unpublish
                      </Button>
                    )}
                    <Button
                      {...restoreFocusTarget}
                      onClick={() =>
                        open({ kind: "deleteVolume", target: v.id })
                      }
                    >
                      Delete
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </section>
    </>
  );
}

export function OperationsSection({ operations }: { operations: Operation[] }) {
  return (
    <section className="panel">
      {operations.length === 0 ? (
        <Empty />
      ) : (
        <Table
          columns={["Operation", "Type", "Target", "Status", "Details"]}
        >
          {operations.map((op) => (
            <tr key={op.id}>
              <td>{op.id}</td>
              <td>{op.type}</td>
              <td>{op.target}</td>
              <td>
                <Status value={op.status} />
              </td>
              <td>{op.error || "—"}</td>
            </tr>
          ))}
        </Table>
      )}
    </section>
  );
}
