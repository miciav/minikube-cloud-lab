# Podman rootless

Verified on Fedora CoreOS 44 ARM64 with Podman 6.1.4 rootless, Kubernetes 1.35.6 and containerd 2.3.4, in a VM with 4 CPUs and 6 GiB RAM. The full autoscaling cycle 1 → 2 → 1 passed, with four load pods Running across two nodes. Physical CPU/RAM limits, HTTP from the host, the oversized-PVC warning and persistence after recreating a pod on its original node were also verified. Cross-node persistence is not an acceptance requirement.

All lab containers in this configuration run under Podman rootless. New Minikube nodes receive an explicit limit of 4096 processes/threads, preserved when CPU/RAM limits are updated. This avoids relying on implicit zero/unlimited PID handling in the Docker-compatible API. A node with `pids.max=1` cannot fork during entrypoint startup; verify the effective cgroup value, not only `podman inspect`.

This configuration uses real persistent directories shared by every node and a simulated allocation budget. It does not impose physical write quotas. Keep enough free disk space in the VM. Use dedicated rootless state; existing Docker/NFS volumes are not migrated.

The Linux VM needs systemd, cgroups v2 with CPU and memory controllers delegated to the student's user, subordinate UID/GID ranges, Podman and a Compose provider. Prepare these as the instructor; students run the lab without sudo. Load kernel modules during VM preparation and persist them across reboots:

```sh
sudo modprobe br_netfilter
sudo modprobe vxlan
printf 'br_netfilter\nvxlan\n' | sudo tee /etc/modules-load.d/cloud-lab.conf
```

Enable the user's API socket with `systemctl --user enable --now podman.socket`. Verify `podman info` reports rootless and cgroups v2.

The CI builds and publishes the cloud and tools images to GHCR for amd64 and arm64 after checks pass on main. Once publication finishes, pull those images in the student VM:

```sh
source scripts/configure-rootless.sh
labcompose --profile tools pull
labcompose up -d cloud
cloud clusters create demo --min-nodes 1 --max-nodes 2
cloud clusters get-credentials demo
lab install autoscaler
lab load start
```

The helper supports Bash and defines `labcompose`, `cloud`, `lab` and containerized `kubectl`. `labcompose` uses the standalone `podman-compose` command when installed, otherwise `podman compose`. No host kubectl installation is required. The wrapper uses the private cluster kubeconfig and mounts the current directory at `/workspace`, so `kubectl apply -f manifest.yaml` works normally. Read and write files relative to the current directory. Host kubeconfig export is optional. `kubectl port-forward pod/nginx 18080:80` starts a separate temporary tools container on the cluster network and publishes port 18080 on the VM's loopback address. It requires no host kubectl. Ctrl+C removes it. Choose an explicit free local port allowed by the rootless host (normally 1024 or above); random ports and non-loopback `--address` values are unsupported. For a remote VM, use an SSH tunnel to access its localhost from your laptop.

`CLOUD_STORAGE_CAPACITY` defaults to `20Gi`. A claim requesting `100Gi` with StorageClass `cloud-standard` remains Pending and produces `StorageCapacityExceeded`. Valid claims use shared directory-backed PVs. Retain keeps both data and the capacity reservation; Delete returns the budget after deleting the volume. Test persistence by recreating a pod on its original node. A directory volume's advertised capacity is an allocation request, not a limit on bytes written.

Delete the cluster with `cloud clusters delete demo` before `labcompose down`. Named volumes survive `down`. Adding `-v` destroys the state and stored data.

Implementation verification is tracked separately from configuration: passing unit tests is not evidence that a particular VM's rootless networking and cgroup delegation work. Run the full lifecycle and check `/sys/fs/cgroup/cpu.max` and `memory.max` inside each node before distributing a VM image.

## Autoscaling check

After `lab load start`, wait for all four replicas and observe a second Ready node. Run `lab load stop` and allow the configured scale-down delay before checking that the worker disappears. The control plane is protected from removal. Keep the storage exercise pod on the control plane when demonstrating scale-down; a pod using local storage can prevent removal of its node.

Rootless nodes retain the distinct MAC addresses assigned by the engine. This avoids duplicate persistent addresses derived by systemd from the base image's identical machine ID. No manual per-node networking changes are required.

## Standalone instances

Standalone instances work in rootless Podman. Verified on Fedora CoreOS 44 ARM64 with Podman 6.2.0 rootless in a VM with 4 CPUs and 6 GiB RAM, cgroups v2 with the cpu, io, memory and pids controllers delegated. The cloud was built from source and ran with the rootless socket. Checks:

- two instances with systemd, SSH and passwordless `sudo`;
- an independent Docker Engine inside an instance, including a long-lived published container;
- instance-to-instance traffic, DNS and Internet;
- distinct machine-ids and the journal;
- persistence across stop/start, CPU/RAM/PID limits, cloud restart reconciliation, and deletion without leftovers.

`systemctl is-system-running` reports `degraded` inside rootless instances: the kernel debug, tracing and configuration mounts and the binfmt automount cannot be mounted in a user namespace. Packages, services and Docker are unaffected. For a few seconds after a start, `systemctl` reports "Failed to connect to bus" while systemd and D-Bus come up. Run `RUNTIME=podman bash tests/instance-feasibility.sh` to check a new VM image before distributing it.

## Existing VM without administrator privileges

Upgrading the operating system is not required just to install Compose. The configuration script installs `podman-compose` in your account automatically when no Compose provider is available. Python 3, pip and access to PyPI are required; no sudo is used:

```sh
systemctl --user enable --now podman.socket
source scripts/configure-rootless.sh
labcompose --profile tools pull
labcompose up -d cloud
```

Source `scripts/configure-rootless.sh` in each terminal; it adds `~/.local/bin` to PATH automatically without duplicating an existing entry. Podman Compose 1.x documents support for Podman 3.4 and later; the helper accepts both old and new cgroup info field names. This does not certify the complete lab on Podman 3.4.4: the full runtime test above used 6.1.4. Missing kernel modules, subordinate UID/GID mappings or cgroup delegation must still be prepared by the VM administrator. If pip is unavailable or user installation is blocked by the Python environment, setup stops with an error; ask for Python/pip or a Compose provider to be included in the VM image. Existing working providers are not reinstalled.

## Automated live verification

For the complete automatic lifecycle, run on the VM from the repository:

```sh
python3 scripts/verify-lab.py
```

Runtime selection is automatic: working Docker takes precedence over rootless
Podman. Use `--runtime podman` to select Podman when both are available.
`source scripts/configure.sh` now also detects Podman and delegates to the
rootless helper; `configure-rootless.sh` remains an explicit Podman entry point.
Both require Bash for Podman configuration. Detection checks the runtime's
reported rootless mode and rejects unsupported Podman rootful or Docker rootless
configurations before changing state.

No cluster needs to be created manually. The script starts the user Podman
socket, runs the configuration preflight, pulls the published images (`--no-pull`
keeps locally built images set in `.env`), starts
Compose and checks service/API readiness. It creates a temporary lab-small
cluster (min 1, max 2), installs the autoscaler, runs all checks, deletes the
cluster and its nodes/relays, then runs Compose down and verifies that no
project containers remain. It does not stop unrelated containers or remove
persistent volumes with `down -v`. Only Python 3 and a working runtime/Compose
provider are required. Kernel modules, UID/GID mappings and cgroup delegation
must already be configured. Use `--runtime docker` for Docker; this also starts
and checks the portal HTTP endpoint and NFS container.

Full lifecycle mode requires an empty platform. If a cluster already exists,
it refuses to delete it or shut its services down. For an existing cluster use
the optional non-destructive mode instead:

```sh
python3 scripts/verify-lab.py --runtime podman --cluster demo
```

This mode requires the platform to be running already and does not start or
stop it. Both modes preserve host kubeconfig and the configured storage budget.
If cluster deletion fails in full mode, management services are kept available
for recovery and the shutdown check fails; inspect the report before retrying.

On an existing cluster, resources are placed in a unique temporary namespace.
Autoscaling is a real load test: allow capacity for two nodes and run outside
other resource-intensive exercises. For a complete 1 → 2 → 1 check, start with
one lab-small node, min=1, max>=2, and an installed autoscaler. Otherwise that
check is reported SKIP. Full lifecycle mode configures these prerequisites
automatically on its temporary cluster. Existing workloads can prevent scale-down, causing a
FAIL; the script never deletes those workloads to force a pass.

Checks cover physical cgroup v2 CPU/RAM limits, deployment readiness, DNS and
ClusterIP HTTP, autoscaling and cross-node networking, PVC persistence on the
original node, oversized-PVC rejection, Delete reclamation, static publishing
and Retain/republication, NodePort/LoadBalancer HTTP, the shell port-forward
wrapper and shutdown, monitoring metrics and the dashboard API. Rootless
storage uses a simulated allocation budget: physical write quotas and
cross-node storage persistence are not claimed. Portal browser interactions,
platform restarts and crash recovery are not tested by this script, to avoid
interrupting an existing deployment.

Allow roughly 20–40 minutes, depending on image downloads and autoscaling.
Polling checks default to 600 seconds each (`--timeout` changes that limit).
Each command also has a bounded timeout. Logs and `report.json` are written to a
private `cloud-lab-verification-*` temporary directory, whose path is printed at
startup. Failed runs collect cloud/node runtime logs even if provisioning fails before
Kubernetes starts, and Kubernetes diagnostics when available, before cleaning
their own resources. Runtime stderr is included in `commands.log`. Keep the report if cleanup fails; the cluster and namespace names
are printed so remaining test resources can be inspected. Ctrl+C also attempts
cleanup; forcibly killing the script or shutting down the VM can leave resources.

Exit codes: **0** all checks passed, **1** failures (including cleanup), **2** no
failures but some checks skipped. A SKIP is not full certification of the VM.
