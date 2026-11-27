# Operating the lab

Use this guide to inspect a running lab, reach its endpoints or recover state. For a first installation, start with the [course setup](../README.md#prepare-the-environment).

## Docker host and credentials

You need Docker Engine on Linux or Docker Desktop on macOS, Compose v2, access
to the local Docker socket. The lab image includes `kubectl` v1.37.3.
The `cloud` and `lab` tools run in containers on both platforms; no Go install
or native project binaries are needed. For the default experiment, provide
at least 6 CPUs and 6 GiB of available memory. The images support Linux amd64
and arm64. Docker Desktop host networking is not required: Compose creates a
dedicated bridge network and publishes the management API on localhost.

The repository and GHCR images are public. Cloning over HTTPS and pulling the
images require no GitHub account, SSH key, or Docker login to `ghcr.io`.
Alternatively, build the images from a checkout using the [development guide](development.md).

`source scripts/configure.sh` prepares the data directories, adds missing
UID/GID/socket-group entries to `.env`, and defines the `cloud` and `lab` shell
commands, a containerized `kubectl`, plus `KUBECONFIG` for this Bash or zsh session. Existing `.env` values are
preserved. Run it in each terminal; it queries runtime info but does not edit shell startup files, pull
images, start Docker services, or create cluster resources. Students run Compose
and all cloud/Kubernetes commands explicitly.

After sourcing `scripts/configure.sh`, `kubectl` runs in the lab tools container with the cloud's private kubeconfig. The current directory is mounted at `/workspace`, so relative manifest paths and `kubectl cp` work without installing kubectl on the host. Use paths relative to this directory; files outside it are not mounted. `port-forward` starts a temporary container on the cluster network and publishes each requested local port on the host's `127.0.0.1`. For example, `kubectl port-forward pod/nginx 18080:80` is reachable at `http://localhost:18080`; Ctrl+C ends the forwarding. Namespace/context flags and multiple mappings are supported, including named remote ports (`18080:http`). Choose explicit local ports from 1 to 65535; random local ports (`:80` or `0:80`) and non-loopback `--address` values are rejected. `kubectl proxy` still listens inside the cloud container's network namespace.

An existing host kubectl installation is unchanged. The shell function takes precedence;
use `command kubectl` to run the host binary with a host-accessible kubeconfig
(for example, `command kubectl --kubeconfig ./cloud-demo.yaml get nodes`).
Run `type kubectl` to check which command your shell selects.

On macOS, start Docker Desktop first. The helper uses BSD `stat` and configures
the group of Docker Desktop's root-owned VM socket. On Linux it reads the local
socket group with GNU `stat`. Existing explicit `.env` values are preserved;
correct copied UID/GID values if they belong to another machine.

New cluster nodes use Google's `https://mirror.gcr.io` cache for public Docker Hub
images, including workers added by the autoscaler. If an image is not cached,
Docker falls back to Docker Hub and its rate limits still apply. The setting is
applied when nodes are created; delete and recreate an existing cluster to use it.
The Docker daemon on your computer is not configured by this project.

New clusters run Kubernetes v1.37.3. A cluster keeps the version it was created with,
workers added later included, so clusters from before the upgrade stay on v1.35.6.
Run `cloud clusters delete` and `cloud clusters create` to move one to v1.37.3.
Recreate such clusters before using the lab images: `kubectl` v1.37.3 and Cluster
Autoscaler v1.37.0 are two minor versions ahead of v1.35, outside the supported skew.
`kubectl get nodes` shows the version of each node.

Wait until `cloud machine-types list` responds before creating the cluster.
The first start downloads node images and Kubernetes components and may take
several minutes. The kubeconfig embeds its certificates, preserves other
contexts, and selects `demo` as the current context. It is published atomically
with permissions `0600`; when `KUBECONFIG` lists multiple files, merged entries
are written to the selected file. With Compose, the CLI writes `/kube/config`,
which is the host's `~/.kube/config`.
The `lab` container shares the running cloud container's network namespace,
so it consumes no address from minikube's node range. It uses the cloud's private
kubeconfig from the read-only state mount. Finish running `lab` commands before
recreating the cloud container. Host `kubectl` uses the exported localhost API
endpoint; the two endpoints use the same cluster identity and embedded certificates.

## Kubernetes dashboard

New clusters automatically include the **YAKD Kubernetes dashboard**, using the
addon bundled with minikube. Wait for its Deployment, then read its automatic
localhost NodePort endpoint:

```sh
kubectl -n yakd-dashboard rollout status deployment/yakd-dashboard --timeout=5m
lab services
```

Open the `127.0.0.1:PORT` shown for `yakd-dashboard` on the computer running
Docker. No forwarding terminal is needed. On a remote VM, use an SSH tunnel
(`ssh -L PORT:127.0.0.1:PORT USER@VM`) with that port.
YAKD uses a cluster-admin service account: keep this local UI private. It requests
128 MiB and is capped at 256 MiB. New clusters also include Metrics Server 0.9.0,
so YAKD can show live Pod CPU and memory usage. Use `lab status yakd` to inspect readiness and `lab uninstall yakd` or `lab install yakd` to remove or restore the dashboard on an existing cluster. An API error installing YAKD fails the create operation and leaves
the partial cluster available for inspection or deletion through `cloud`.

## Cloud portal

Run `lab install portal` and open <http://localhost:3000>. On a remote VM, run `ssh -L 3000:127.0.0.1:3000 student@course-vm` from your own computer. The separate `portal`
container serves an Azure-inspired interface for the existing cloud API. It has
no Docker socket, kubeconfig mount or extra privileges. Both the portal and
management API are published only on localhost.

Use it to create/delete clusters, download kubeconfig, inspect machine types and
machines, create/publish/unpublish/delete cloud volumes, view the allocation budget,
and follow cloud operations. Destructive actions require confirmation. Cluster deletion and volume publication
also check the selected cluster generation, so a cluster recreated under the same
name is not affected by an old dialog. A downloaded
kubeconfig is a file: the browser does not merge it into `~/.kube/config`. Use
`command kubectl --kubeconfig ./cloud-demo.yaml get nodes` or the existing
`cloud clusters get-credentials demo` command when you want to merge credentials.

A cluster's **Monitoring** tab shows Ready nodes, actual CPU/memory usage, resource
requests versus allocatable capacity, Pending/unhealthy Pod counts and the cloud
storage allocation budget. Allocated storage is reserved capacity, not bytes written.
Metrics refresh every 15 seconds while the page is visible. Missing, partial or stale
measurements are labelled; the cluster tab works independently of the optional history module.

Install `monitoring` to collect samples every 15 seconds. Select **Monitoring** in the main navigation and choose 15 minutes, one hour or 24 hours. Cluster usage is shown in cores and MiB. Queue gauges estimate available, in-flight and delayed messages. Bucket gauges show object count and logical bytes. A gap indicates a missed observation. A measured zero means the source actually returned zero. History survives closing the browser and `lab uninstall monitoring`. Reinstall to inspect retained samples within the retention period. To erase the owned volume, run `lab uninstall monitoring` followed by `lab purge monitoring --yes`.

The **Costs** section shows the same figures as `cloud billing` for three periods: Since reset, Last hour and Last 30 minutes.

The **Kubernetes** tab embeds the automatically installed YAKD dashboard using its
existing localhost Service endpoint. YAKD provides workload details and logs; it is
not rewritten or proxied under a different URL path. A separate link opens the same
dashboard in a new tab. On a remote Linux VM, forward port 3000 and the dashboard's
reported localhost port with SSH, preserving the dashboard port number.

Autoscaler installation and load-generation exercises remain terminal operations:
the portal has no `lab` controls. CLI and portal changes affect the same resources.
The interface is intended for a trusted local teaching environment, without multiuser
login or public network exposure.

Metrics Server is pinned by image digest and installed by the cloud through the
Kubernetes API. Minikube's generated kubelet certificates lack IP SANs, so this lab
uses `--kubelet-insecure-tls` for **internal kubelet scraping only**. This is a lab
limit, not a production configuration; it does not disable certificate checking
for the cloud API or your kubeconfig. Allow a short startup interval for the first
measurements. A manifest installation error fails cluster creation; an initially
empty Metrics API is shown as unavailable until observations arrive.

## State and reset

Compose mounts `~/.minikube-cloud-lab` at `/data` and `~/.kube` at `/kube`.
`state.json` stores clusters, machines, and operations; `minikube/` contains the
isolated backend state. The service does not use your personal minikube profile.
After a restart, it marks interrupted operations as ERROR and reconciles machines.
If Docker cannot be observed, the service preserves machine ownership and starts
the API with the cluster in ERROR so cleanup remains possible. A failed addition
may already have created a machine: it remains recorded. If observation fails,
its capacity stays reserved until reconciliation or cluster deletion.

Delete the cluster before `docker compose down`, so Compose can remove its
network. Stopping the service preserves data and machines; if nodes remain,
Docker refuses to remove the network while it is in use.
**Before deleting the Docker state directory**, uninstall any instance groups, delete standalone instances you own through the cloud, and back up data you need. Run `cloud clusters delete NAME` for the managed cluster and
wait for it to finish. Removing only the files would lose the references to
Docker resources. Then, to clear the operation history as well:

```sh
docker compose down
rm -rf "$HOME/.minikube-cloud-lab"
```

The kubeconfig context remains after cluster deletion; other contexts are
preserved. Do not delete all of `~/.kube` to reset this lab.

The cloud controls the local Docker socket. Compose publishes its management
API at `127.0.0.1:8080`. Externalgrpc port 9090 stays inside the dedicated Docker
network: nodes resolve `host.minikube.internal` to the cloud container's stable
address. The MVP has no management authentication or TLS; use a trusted lab host.

The default network is `172.30.0.0/23`; cloud uses `172.30.0.254`, outside
minikube's automatic node-address range. If that subnet conflicts with another
network, delete the cluster and run `docker compose down`, then set a matching
subnet, gateway, and cloud address in `.env`, for example:

```dotenv
CLOUD_DOCKER_SUBNET=172.29.240.0/23
CLOUD_DOCKER_GATEWAY=172.29.240.1
CLOUD_DOCKER_IP=172.29.240.254
CLOUD_NFS_IP=172.29.240.253
```

Use an aligned private `/23`: gateway `.1` and cloud `.254` in its first `/24`,
and Service relays on hosts 1–254 of the second `/24`. Update all four values
together and use an available NFS address. The instance autoscaling installer
derives two addresses from the cloud IP.
It uses the middle two hosts of the /23, outside both the node and relay pools.
For direct Compose profile commands on the example subnet, set
`CLOUD_INSTANCE_LB_IP=172.29.240.255` and `CLOUD_INSTANCE_METRICS_IP=172.29.241.0`. The explicit gateway is
required for the minikube network API on Linux as well as Docker Desktop. Start
Compose again and create a new cluster. Before upgrading an older host-network
installation, delete its cluster with the old cloud, run `docker compose down`,
pull the updated images, and run `docker compose up -d` before recreating
the cluster. Existing clusters do not migrate between networks automatically. The Service
exposure network expands older `/24` installations to `/23`: delete the old
cluster before `docker compose down`, then recreate it using the updated network.
Older versions could leave worker volumes after scale-down. Before reusing a
cluster name, inspect any volume named `NAME-mNN` with `docker volume inspect`.
Remove it only after confirming it belongs to the deleted lab and no container
uses it; the cloud does not automatically delete historical orphan volumes.

## Personal images

`cloud images create` stores each personal image in the local container runtime: an image plus a volume holding a copy of `/var`. Each clone launched from it receives another copy, so disk usage grows with the size of `/var` and with the number of instances. The cloud allows at most 8 personal images. `docker image prune -a` or `docker volume prune` (`podman` on Podman) can remove them behind the cloud's back; the next start then marks the image `ERROR`. Delete it with `cloud images delete NAME`, never with the runtime, and capture it again. See the [golden images tutorial](tutorials/compute/golden-images.md).
Downgrading the cloud to a version without personal images drops the image catalog from the state on its first save and orphans the `localhost/minikube-cloud-lab/image-*` images and `mcl-image-*-var` volumes. Delete personal images through the cloud before downgrading.

## Cloud storage

The cloud automatically installs storage classes for dynamically provisioned NFS volumes. `cloud-standard` deletes released volumes; `cloud-retain` preserves them for recovery. Deleting a cluster conservatively retains its remaining volumes, which can be removed explicitly. `cloud volumes list` displays their state. See the [storage tutorial](tutorials/storage/persistent-volumes.md) for creation and recovery across clusters.

The NFS data is stored in a persistent named Docker volume. `docker compose down` keeps it; `docker compose down -v` removes it and all stored data. Each new volume has a separate bounded ext4 filesystem exported over NFS. Writes stop with a disk-full error when its virtual disk is full. Capacity includes filesystem overhead, is rounded up to MiB, and has a 16Mi minimum. `cloud storage status` reports the global allocation budget (default `20Gi`, configurable with `CLOUD_STORAGE_CAPACITY` in `.env`). Allocated and retained volumes count until permanently deleted; unused capacity inside a volume is still allocated. The budget does not reserve physical host disk space.

Administrators can use `cloud volumes create --capacity 2Gi`, `cloud volumes publish ID --cluster demo`, and `cloud volumes unpublish ID` for static provisioning with `cloud-static`. Publishing prevents deletion or use by another cluster; unpublishing preserves the data.

The NFS container needs `SYS_ADMIN`, `DAC_READ_SEARCH`, and loop-device access to mount the backing filesystems. AppArmor confinement is disabled only for that NFS container so Ubuntu permits the mounts; the default seccomp policy remains active. It does not use privileged mode or change host settings. The lab NFS export is for a trusted local network and is not a multi-tenant storage service.

Existing directory-backed volumes remain legacy storage: `ENFORCED=false`. They
are not migrated or reformatted automatically. Inventory and cleanup remain
available, but new allocations are blocked until an explicit offline migration
or removal of the old volumes. If you choose to delete all legacy volumes,
restart NFS afterward with `docker compose restart nfs`. Keep backups; do not use
`down -v` as a migration tool.

## Course service state

Each managed service owns a separate Compose project and one named volume. Its private bootstrap settings live under `~/.minikube-cloud-lab/services/<runtime>/<project>-<module>/`. Preserve the volume and this directory together when moving a lab. Reuse the installed PostgreSQL major version when restoring its volume. The core project's Compose shutdown and cluster deletion leave these service projects independent. To release resources use `lab uninstall NAME`; use `lab status NAME` to see retained data.

For diagnostics inspect the service container with the runtime's logs command. Filter container inventory by the `com.docker.compose.project` label. A failed first install can be retried after resolving image, port or filesystem errors. Keep the existing volume when credentials are missing and restore the matching private configuration.

## Usage and cost ledger

The cloud meters fictitious costs: `lab-small` 0.10 EUR per hour, `lab-medium` 0.40 EUR per hour, and storage 0.10 EUR per GiB per month (730 hours). Cluster machines (including the control plane) accrue from creation until removal. Instances accrue except while `STOPPED`; their root disk is free. Kubernetes volumes (bound or retained) and data disks accrue storage for their size until deleted. Managed services, load balancers and Pods are not billed.

The ledger is `usage.json` in `CLOUD_BILLING_DIR`. Compose sets it to `/data/billing`, on the `minikube-cloud-lab-billing` volume (`minikube-cloud-lab-rootless-billing` with Podman). `docker compose down` keeps it and time keeps accruing for running resources. `labcompose down -v` removes the volume, so the count restarts from zero; resources that survive start accruing again from the reset. A corrupt ledger is replaced by a fresh one. The meter never limits or blocks anything.

`cloud billing` and `GET /v1/billing?since=` read it. The CLI and portal show costs with six decimals and mark each resource `accruing` (Accruing) while billed or `ended` (Ended) otherwise. `since` is a Go duration (`30m`) or an RFC3339 time; future, negative or unparsable values return 400, and a time before the reset is clamped to the reset.
