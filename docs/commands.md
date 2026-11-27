# Command reference

Run the commands from a checkout after `source scripts/configure.sh`. `cloud` manages infrastructure, `kubectl` acts on the selected cluster, and `lab` manages teaching modules and load. Use `--help` on a command for its flags.

| Command | Purpose / options |
| --- | --- |
| `lab install portal` / `lab status portal` / `lab uninstall portal` | Start, inspect or remove the local course console. |
| `lab install monitoring` / `lab status monitoring` / `lab uninstall monitoring` | Start, inspect or stop collection. History survives uninstall. |
| `lab purge monitoring --yes` | Erase the disabled module’s history. |
| `cloud serve` | Starts the management API and externalgrpc service; `--data-dir`, `--api` (loopback), `--grpc` |
| `cloud machine-types list` | Displays the catalog with `PRICE_EUR_HOUR` |
| `cloud billing [--since 30m\|RFC3339]` | Shows the cost since the last reset or since a Go duration or RFC3339 time, per resource (`STATUS` `accruing` or `ended`, costs with six decimals) with a `TOTAL` line; see [costs](tutorials/billing/costs.md) |
| `cloud clusters create NAME` | `--machine-type lab-small`, `--min-nodes 1`, `--max-nodes 3`, `--async` |
| `cloud clusters list` | Lists the managed cluster |
| `cloud clusters describe NAME` | Displays machine type, size limits, and status |
| `cloud clusters resize NAME --nodes N` | Adds workers and sets min=max=N, including the control plane; optional `--async`; shrinking is rejected |
| `cloud clusters delete NAME` | Deletes the cluster and its machines; `--async` |
| `cloud clusters get-credentials NAME` | Updates `$KUBECONFIG` or `~/.kube/config` |
| `cloud machines list` | Optional `--cluster NAME` filter |
| `cloud volumes list` / `cloud volumes delete ID` | List volumes or delete an available volume |
| `cloud volumes create --capacity SIZE` / `publish ID --cluster NAME` / `unpublish ID` | Allocate and publish a static volume or release its cluster binding |
| `cloud images list` | Displays the standalone instance images |
| `cloud instances create NAME` | `--image ubuntu-26.04` (or `debian-12`), `--machine-type lab-small`, `--async` |
| `cloud instances list` / `describe NAME` | Lists instances or displays one, with status and IP |
| `cloud instances stop NAME` / `start NAME` / `delete NAME` | Lifecycle requests; `--async` |
| `cloud instances ssh NAME [-- COMMAND...]` | Opens a shell, or runs a command and returns its exit status |
| `cloud operations list` | Displays the request history, newest first |
| `cloud operations describe ID` | Displays one operation |
| `lab install MODULE` / `lab status MODULE` / `lab uninstall MODULE` | Manage `autoscaler`, `instance-autoscaling` or `yakd`; see [scope and removal](../README.md#optional-modules) |
| `lab load start` / `lab load stop` | Creates / deletes the workload; `start --replicas N` |
| `lab services` | Show local endpoints for exposed Kubernetes Services |
| `cloud groups create NAME` / `resize NAME` / `delete NAME` | Create a group, change desired capacity or remove its members |
| `cloud groups list` / `describe NAME` | Inspect desired and ready capacity |
| `cloud disks create NAME` / `attach NAME` / `detach NAME` / `delete NAME` | Manage a standalone instance data disk |
| `cloud disks list` / `cloud disks shell INSTANCE DISK` | Inspect disks or open the rootless disk console |
| `cloud storage status` | Inspect the Kubernetes volume allocation budget |

## Growing an existing cluster

To grow an existing lab without recreating its cluster or volumes:

```sh
cloud clusters resize deck3 --nodes 3
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes -o wide
```

Use the actual cluster name. This command sets both size limits to the requested
total and creates only the missing workers. It preserves the control plane,
machine type, credentials, workloads and volumes. It does not redistribute
existing Pods. A completed repeated request at the same size creates nothing;
a smaller size is rejected. Do not use this command to preserve an autoscaling
range: it deliberately switches to a fixed node count. Wait for running
operations to finish before another resize. If worker creation fails, inspect
`cloud operations list` and resolve the reported error, then retry the same size;
successfully created workers remain tracked. An unresolved machine requires
reconciliation before retrying. `--async` prints one operation ID per new worker,
or no IDs when no worker is needed; accepted work survives client disconnection.

New workers are automatically labeled `node-role.kubernetes.io/worker=worker`
during cluster creation, manual growth, and autoscaling, so `kubectl get nodes`
shows their `worker` role. Existing nodes are not relabeled automatically.

New workers inherit the existing machine type. Three `lab-small` nodes have
aggregate limits of 6 CPUs / 6 GiB; three `lab-medium` nodes have limits of
12 CPUs / 24 GiB. These limits do not create physical capacity: check the Docker
host allocation and actual workload memory before increasing the node count.

The management API accepts `POST /v1/clusters/{name}/resize` with `{"nodes":3}`
and returns the new worker operations. Limits and reservations are persisted in
one snapshot before provisioning starts. The optional strong `If-Match` header
checks cluster identity before accepting this mutation.

The management API returns the cluster generation as an ETag on
`GET /v1/clusters/{name}`. Portal mutations send it as a single strong `If-Match`
header on cluster DELETE and volume publish; a mismatch returns 412. Existing
CLI/API clients without this optional header retain their current behavior.

The cloud CLI uses `--api-url` or `CLOUD_API_URL` (default
`http://127.0.0.1:8080`). `lab` accepts `--kubeconfig` and `--context`.
Create and delete commands wait for completion; `--async` returns the operation
ID immediately. Ctrl-C stops the client's wait, while the operation continues
in the service.

## Managed services

Use `lab install NAME`, `lab status NAME`, `lab client NAME ...` and `lab uninstall NAME` after sourcing the shell helper. Each module uses its own Compose project, network and named data volume. Each install prints its connection settings directory. Temporary clients join the service project’s private network; use the published loopback endpoint from host applications. Override the host port before installation with `OBJECT_STORAGE_PORT`, `QUEUE_PORT` or `DATABASE_PORT`. Keep the same project name and runtime when returning to your data.

To permanently remove **all** retained data and credentials for one module, uninstall it first, then run `lab purge NAME --yes`. A fresh install generates new credentials. The local account controls the runtime and its private configuration directory. The queue endpoint is for a trusted single-team host; loopback is accessible to other accounts on that host.

## Infrastructure and configuration tools

After sourcing the helper, `tofu`, `ansible` and `ansible-playbook` use the course lab image on the cloud network. The current directory is mounted read/write at `/workspace` for state, playbooks and inventory. `labtool ssh-keygen` and `labtool python3` use the same environment. Start cloud first. The bundled OpenTofu provider installs from a filesystem mirror inside the image. Follow the [mixed deployment exercise](../lab/capstone/README.md).
