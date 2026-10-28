import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import {
  Button,
  useRestoreFocusTarget,
  TabList,
  Tab,
  Dialog,
  DialogSurface,
  DialogBody,
  DialogTitle,
  DialogContent,
  DialogActions,
  Field,
  Input,
  FluentProvider,
  webLightTheme,
  Title1,
  Title2,
  Badge,
  Spinner,
} from "@fluentui/react-components";
import { request, downloadKubeconfig } from "./api";
import { Monitoring } from "./Monitoring";
import { Dashboard } from "./Dashboard";
import { useResource } from "./useResource";
import type {
  Cluster,
  Machine,
  MachineType,
  Volume,
  Operation,
  StorageCapacity,
} from "./types";

type Action = {
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
const headings = {
  createCluster: "Create cluster",
  deleteCluster: "Delete cluster",
  createVolume: "Create volume",
  deleteVolume: "Delete volume",
  publish: "Publish volume",
  unpublish: "Unpublish volume",
};
const labels = {
  createCluster: "Create",
  deleteCluster: "Delete",
  createVolume: "Create",
  deleteVolume: "Delete",
  publish: "Publish",
  unpublish: "Unpublish",
};
function Table({
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
function Status({ value }: { value: string }) {
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
function Empty() {
  return <p className="empty">No resources yet.</p>;
}

export function App() {
  return (
    <FluentProvider theme={webLightTheme} className="portal">
      <CloudApp />
    </FluentProvider>
  );
}
function CloudApp() {
  const restoreFocusTarget = useRestoreFocusTarget();
  const [route, setRoute] = useState(
    () => window.location.hash.slice(2) || "overview",
  );
  const [refresh, setRefresh] = useState(0);
  const [action, setAction] = useState<Action | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const [notice, setNotice] = useState("");
  const [form, setForm] = useState({
    name: "",
    machineType: "lab-small",
    minNodes: "1",
    maxNodes: "3",
    capacity: "1Gi",
    cluster: "",
    clusterGeneration: "",
    confirm: "",
  });
  const clusterData = useResource<Cluster[]>("clusters", refresh),
    machineData = useResource<Machine[]>("machines", refresh),
    typeData = useResource<MachineType[]>("machine-types", refresh),
    volumeData = useResource<Volume[]>("volumes", refresh),
    operationData = useResource<Operation[]>("operations", refresh),
    storageData = useResource<StorageCapacity>("storage", refresh);
  const clusters = clusterData.data ?? [],
    machines = machineData.data ?? [],
    types = typeData.data ?? [],
    volumes = volumeData.data ?? [],
    operations = operationData.data ?? [];
  const error =
    clusterData.error ||
    machineData.error ||
    typeData.error ||
    volumeData.error ||
    operationData.error;
  useEffect(() => {
    const change = () => setRoute(window.location.hash.slice(2) || "overview");
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  useEffect(() => {
    if (!dialogOpen || !action || clusterData.data === null) return;
    const name =
      action.kind === "deleteCluster"
        ? action.target
        : action.kind === "publish"
          ? form.cluster
          : undefined;
    const generation =
      action.kind === "deleteCluster"
        ? action.generation
        : form.clusterGeneration;
    if (
      name &&
      !clusterData.data.some(
        (c) => c.name === name && c.createdAt === generation,
      )
    ) {
      setDialogOpen(false);
      setNotice(
        "Cluster changed or was deleted. Review the current resource before continuing.",
      );
    }
  }, [
    clusterData.data,
    dialogOpen,
    action,
    form.cluster,
    form.clusterGeneration,
  ]);
  const section = route.split("/")[0],
    selected = clusters.find((c) => route === `clusters/${c.name}`);
  function open(next: Action) {
    setForm({
      name: "",
      machineType: types[0]?.name ?? "lab-small",
      minNodes: "1",
      maxNodes: "3",
      capacity: "1Gi",
      cluster: clusters.find((c) => c.status === "RUNNING")?.name ?? "",
      clusterGeneration:
        clusters.find((c) => c.status === "RUNNING")?.createdAt ?? "",
      confirm: "",
    });
    setActionError("");
    setAction({
      ...next,
      generation:
        next.kind === "deleteCluster"
          ? clusters.find((c) => c.name === next.target)?.createdAt
          : undefined,
    });
    setDialogOpen(true);
  }
  function field(name: keyof typeof form, value: string) {
    setForm((v) => ({
      ...v,
      [name]: value,
      ...(name === "cluster"
        ? {
            clusterGeneration:
              clusters.find((c) => c.name === value)?.createdAt ?? "",
          }
        : {}),
    }));
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!action || busy) return;
    setBusy(true);
    setActionError("");
    const id = encodeURIComponent(action.target ?? "");
    try {
      const generation =
        action.kind === "deleteCluster"
          ? action.generation
          : action.kind === "publish"
            ? form.clusterGeneration
            : undefined;
      if (
        (action.kind === "deleteCluster" || action.kind === "publish") &&
        !generation
      )
        throw Error(
          "Cluster generation unavailable; refresh before continuing.",
        );
      switch (action.kind) {
        case "createCluster": {
          const op = await request<Operation>("clusters", {
            method: "POST",
            body: JSON.stringify({
              name: form.name,
              machineType: form.machineType,
              minNodes: Number(form.minNodes),
              maxNodes: Number(form.maxNodes),
            }),
          });
          setNotice(`Operation ${op.id} accepted.`);
          break;
        }
        case "deleteCluster": {
          const op = await request<Operation>(`clusters/${id}`, {
            method: "DELETE",
            headers: { "If-Match": JSON.stringify(generation) },
          });
          setNotice(`Operation ${op.id} accepted.`);
          if (route === `clusters/${action.target}`)
            window.location.hash = "/clusters";
          break;
        }
        case "createVolume": {
          const v = await request<Volume>("volumes", {
            method: "POST",
            body: JSON.stringify({ capacity: form.capacity }),
          });
          setNotice(`Volume ${v.id} created.`);
          break;
        }
        case "deleteVolume":
          await request(`volumes/${id}`, { method: "DELETE" });
          setNotice("Volume deleted.");
          break;
        case "publish":
          await request(`volumes/${id}/publish`, {
            method: "POST",
            body: JSON.stringify({ cluster: form.cluster }),
            headers: { "If-Match": JSON.stringify(generation) },
          });
          setNotice("Volume published.");
          break;
        case "unpublish":
          await request(`volumes/${id}/unpublish`, { method: "POST" });
          setNotice("Volume unpublished; data retained in the cloud.");
          break;
      }
      setDialogOpen(false);
      setRefresh((v) => v + 1);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : "Request failed");
    } finally {
      setBusy(false);
    }
  }
  async function credentials(name: string) {
    try {
      await downloadKubeconfig(name);
      setNotice(
        "Kubeconfig downloaded. Import it explicitly in your terminal when needed.",
      );
    } catch (e) {
      setNotice(e instanceof Error ? e.message : "Download failed");
    }
  }
  const deleting =
    action?.kind === "deleteCluster" || action?.kind === "deleteVolume";
  return (
    <>
      <header>
        <a href="#/" className="brand">
          <span className="brand-icon" aria-hidden="true">
            ▦
          </span>{" "}
          Cloud Lab
        </a>
        <span className="header-context">Local cloud portal</span>
        <Badge appearance="outline" color="informative">
          Learning environment
        </Badge>
      </header>
      <div className="workspace">
        <nav aria-label="Cloud navigation">
          <div className="nav-caption">YOUR CLOUD</div>
          {["Overview", "Clusters", "Machines", "Volumes", "Operations"].map(
            (label) => (
              <a
                key={label}
                href={`#/${label.toLowerCase()}`}
                aria-current={
                  section === label.toLowerCase() ? "page" : undefined
                }
              >
                <span aria-hidden="true">
                  {
                    {
                      Overview: "▦",
                      Clusters: "◇",
                      Machines: "▣",
                      Volumes: "▤",
                      Operations: "↻",
                    }[label as "Overview"]
                  }
                </span>
                {label}
              </a>
            ),
          )}
          <div className="nav-note">
            Cloud resources
            <br />
            <small>Managed on your computer</small>
          </div>
        </nav>
        <main>
          <div className="breadcrumb">
            Cloud Lab / {section === "overview" ? "Overview" : section}
            {selected ? ` / ${selected.name}` : ""}
          </div>
          {notice && (
            <div className="notice" role="status">
              {notice}
              <Button
                {...restoreFocusTarget}
                size="small"
                appearance="transparent"
                onClick={() => setNotice("")}
                aria-label="Dismiss notification"
              >
                ×
              </Button>
            </div>
          )}
          {error && (
            <div className="error" role="alert">
              {error}{" "}
              <Button
                {...restoreFocusTarget}
                onClick={() => setRefresh((v) => v + 1)}
              >
                Retry
              </Button>
            </div>
          )}
          <div className="page-heading">
            <div>
              <Title1>
                {selected
                  ? selected.name
                  : ({
                      overview: "Overview",
                      clusters: "Kubernetes clusters",
                      machines: "Machines",
                      volumes: "Storage volumes",
                      operations: "Cloud operations",
                    }[section] ?? "Cluster unavailable")}
              </Title1>
              <p className="subtitle">
                {selected
                  ? "Manage cluster resources and inspect live health."
                  : "Manage your cloud infrastructure and follow its operations."}
              </p>
            </div>
            <Button
              {...restoreFocusTarget}
              onClick={() => setRefresh((v) => v + 1)}
            >
              Refresh
            </Button>
          </div>
          {section === "overview" && (
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
                    {storageData.data?.allocated ?? "Unavailable"}
                  </strong>
                  <small>
                    of {storageData.data?.total ?? "—"} cloud budget
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
          )}
          {section === "clusters" && !route.includes("/") && (
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
          )}
          {section === "clusters" &&
            route.includes("/") &&
            (selected ? (
              <ClusterDetails
                key={`${selected.name}:${selected.createdAt}`}
                cluster={selected}
                machines={machines.filter((m) => m.cluster === selected.name)}
                storage={storageData.data}
                refresh={refresh}
                onCredentials={() => void credentials(selected.name)}
                onDelete={() =>
                  open({ kind: "deleteCluster", target: selected.name })
                }
              />
            ) : (
              <p role="status">
                {clusterData.data === null
                  ? "Loading cluster…"
                  : "Cluster unavailable or deleted."}
              </p>
            ))}
          {section === "machines" && (
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
          )}
          {section === "volumes" && (
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
                {storageData.data ? (
                  <dl className="details">
                    <div>
                      <dt>Total</dt>
                      <dd>{storageData.data.total}</dd>
                    </div>
                    <div>
                      <dt>Allocated capacity</dt>
                      <dd>{storageData.data.allocated}</dd>
                    </div>
                    <div>
                      <dt>Available</dt>
                      <dd>{storageData.data.available}</dd>
                    </div>
                  </dl>
                ) : (
                  <p>{storageData.error || "Loading storage budget…"}</p>
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
                            {v.enforced ? "Enforced capacity" : "Legacy volume"}
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
          )}
          {section === "operations" && (
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
          )}
          {!clusterData.data && !error && (
            <Spinner size="small" label="Loading cloud resources…" />
          )}
        </main>
      </div>
      <Dialog
        open={dialogOpen}
        onOpenChange={(_, v) => {
          if (!v.open && !busy) setDialogOpen(false);
        }}
      >
        <DialogSurface>
          <form onSubmit={submit}>
            <DialogBody>
              <DialogTitle>{action ? headings[action.kind] : ""}</DialogTitle>
              <DialogContent className="form-fields">
                {action?.kind === "createCluster" && (
                  <>
                    <Field label="Cluster name" required>
                      <Input
                        aria-label="Cluster name"
                        value={form.name}
                        onChange={(_, v) => field("name", v.value)}
                        required
                        pattern="[a-z][a-z0-9-]*"
                        maxLength={63}
                      />
                    </Field>
                    <Field label="Machine type">
                      <select
                        aria-label="Machine type"
                        value={form.machineType}
                        onChange={(e) => field("machineType", e.target.value)}
                      >
                        {types.map((t) => (
                          <option key={t.name} value={t.name}>
                            {t.name} · {t.cpus} CPU / {t.memoryMiB} MiB
                          </option>
                        ))}
                      </select>
                    </Field>
                    <div className="form-row">
                      <Field label="Minimum nodes">
                        <Input
                          aria-label="Minimum nodes"
                          type="number"
                          min={1}
                          max={2147483647}
                          required
                          value={form.minNodes}
                          onChange={(_, v) => field("minNodes", v.value)}
                        />
                      </Field>
                      <Field label="Maximum nodes">
                        <Input
                          aria-label="Maximum nodes"
                          type="number"
                          min={Number(form.minNodes) || 1}
                          max={2147483647}
                          required
                          value={form.maxNodes}
                          onChange={(_, v) => field("maxNodes", v.value)}
                        />
                      </Field>
                    </div>
                    <p className="muted">
                      The node range includes the control plane. Small machines
                      have 2 GiB of memory.
                    </p>
                  </>
                )}
                {action?.kind === "createVolume" && (
                  <Field label="Capacity" required>
                    <Input
                      aria-label="Capacity"
                      value={form.capacity}
                      required
                      onChange={(_, v) => field("capacity", v.value)}
                    />
                    <small>
                      For example 1Gi. Capacity is enforced by the cloud.
                    </small>
                  </Field>
                )}
                {action?.kind === "publish" && (
                  <>
                    <p>
                      Make {action.target} available as a PersistentVolume in
                      the selected cluster.
                    </p>
                    <Field label="Cluster">
                      <select
                        aria-label="Cluster"
                        value={form.cluster}
                        required
                        onChange={(e) => field("cluster", e.target.value)}
                      >
                        <option value="" disabled>
                          Select a running cluster
                        </option>
                        {clusters
                          .filter((c) => c.status === "RUNNING")
                          .map((c) => (
                            <option key={c.name}>{c.name}</option>
                          ))}
                      </select>
                    </Field>
                  </>
                )}
                {action?.kind === "unpublish" && (
                  <p>
                    Remove the PersistentVolume for {action.target} from
                    Kubernetes. Cloud data is retained. A volume in use cannot
                    be unpublished.
                  </p>
                )}
                {deleting && (
                  <>
                    <p>
                      {action?.kind === "deleteVolume"
                        ? "Permanently delete this cloud volume and its data."
                        : "Delete the cluster and its machines. Retained cloud volumes remain allocated."}
                    </p>
                    <p>
                      Type <strong>{action?.target}</strong> to confirm.
                    </p>
                    <Field label="Confirm resource name">
                      <Input
                        aria-label="Confirm resource name"
                        value={form.confirm}
                        onChange={(_, v) => field("confirm", v.value)}
                      />
                    </Field>
                  </>
                )}
                {actionError && (
                  <div className="error" role="alert">
                    {actionError}
                  </div>
                )}
              </DialogContent>
              <DialogActions>
                <Button disabled={busy} onClick={() => setDialogOpen(false)}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  appearance="primary"
                  disabled={
                    busy ||
                    (deleting && form.confirm !== action?.target) ||
                    (action?.kind === "publish" && !form.cluster)
                  }
                >
                  {busy ? "Working…" : action ? labels[action.kind] : ""}
                </Button>
              </DialogActions>
            </DialogBody>
          </form>
        </DialogSurface>
      </Dialog>
    </>
  );
}

function ClusterDetails({
  cluster,
  machines,
  storage,
  refresh,
  onCredentials,
  onDelete,
}: {
  cluster: Cluster;
  machines: Machine[];
  storage: StorageCapacity | null;
  refresh: number;
  onCredentials: () => void;
  onDelete: () => void;
}) {
  const restoreFocusTarget = useRestoreFocusTarget();
  const [tab, setTab] = useState("Overview");
  return (
    <>
      <div className="toolbar">
        <Button
          {...restoreFocusTarget}
          onClick={onCredentials}
          disabled={cluster.status !== "RUNNING"}
        >
          Download kubeconfig
        </Button>
        <Button {...restoreFocusTarget} onClick={onDelete}>
          Delete cluster
        </Button>
      </div>
      <TabList
        className="tabs"
        aria-label="Cluster views"
        selectedValue={tab}
        onTabSelect={(_, data) => setTab(String(data.value))}
      >
        {["Overview", "Monitoring", "Kubernetes"].map((t) => (
          <Tab key={t} value={t}>
            {t}
          </Tab>
        ))}
      </TabList>
      {tab === "Kubernetes" ? (
        <Dashboard cluster={cluster.name} refresh={refresh} />
      ) : tab === "Monitoring" ? (
        <Monitoring
          cluster={cluster.name}
          storage={storage}
          refresh={refresh}
        />
      ) : (
        <section className="panel">
          <Title2>Cluster overview</Title2>
          <dl className="details">
            <div>
              <dt>Status</dt>
              <dd>
                <Status value={cluster.status} />
              </dd>
            </div>
            <div>
              <dt>Machine type</dt>
              <dd>{cluster.machineType}</dd>
            </div>
            <div>
              <dt>Node range</dt>
              <dd>
                {cluster.minMachines}–{cluster.maxMachines}
              </dd>
            </div>
            <div>
              <dt>Machines</dt>
              <dd>{machines.length}</dd>
            </div>
          </dl>
        </section>
      )}
    </>
  );
}
