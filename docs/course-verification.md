# Verify the course environment

Run the checks against the runtime and images you will distribute. Use the general suite for the Kubernetes exercises and the instance group smoke for the HTTP autoscaling exercise.

## Annual acceptance on the IT-provided VM

Log in to the actual teaching VM as the student account. Complete the [rootless Podman setup](podman-rootless.md#prepare-an-existing-linux-vm), install the course version and select its images. Run the check before creating any class resources. It uses the configured images, including local build overrides. Allow exclusive use of the lab, access to the exercise image registries and package mirrors, and enough capacity for the two lab-medium nodes used by MySQL (4 CPUs and 8 GiB each, plus services). The host also needs Python 3 with PyYAML, Bash, zsh, curl, OpenSSL and ssh-keygen.

From the checkout run:

```sh
python3 scripts/verify-course-vm.py
```

The command starts the user socket and cloud, checks the inventory, then runs the following suites in sequence:

| Suite | Evidence |
| --- | --- |
| Runner tests and Lecture 02 validation | Verification logic, manifest consistency and worker setup in Bash and zsh. Lecture 02's interactive slide exercises are a separate manual rehearsal. |
| Managed services | Install Garage, ElasticMQ and PostgreSQL individually, check persistence and isolation, run the CSV exercise, then remove the test installations. Existing service data stops this stage. |
| Standalone compute and storage | Test cloud-init and SSH on both Ubuntu and Debian. Write a disk on one instance and read it on another through the QEMU console. |
| Instance autoscaling | Generate HTTP load, check scale-out and scale-in, simulate a metrics outage, replace a failed member and uninstall the module. |
| Lecture 03 | Exercise HPA, VPA, Cluster Autoscaler, KEDA, custom resources and the MySQL Operator on disposable clusters. |
| General platform | Verify real Kubernetes node scaling, networking, monitoring, storage, standalone instances and clean shutdown. |

Reserve a few hours for the first run. Exit 0 and a final **PASS** indicate that every listed suite completed. A failed or skipped suite stops the run and gives a nonzero exit. The private report directory under `~/.local/state/minikube-cloud-lab/` (or `$XDG_STATE_HOME`) contains the summary, one numbered log per stage, and the detailed Kubernetes reports. Keep that directory with the course version and VM specifications for the annual handover.

On success the lab's Compose stack is stopped. On failure, inspect the indicated step and remaining lab resources before retrying. The VM stays available throughout. Real guest reboot recovery is a separate check on a disposable Multipass guest.

## Kubernetes and cloud lifecycle

For the complete automatic lifecycle, run on the Docker host or inside the prepared Podman VM from the repository:

```sh
python3 scripts/verify-lab.py
```

Runtime selection is automatic: working Docker takes precedence over rootless
Podman. Use `--runtime podman` to select Podman when both are available.
`source scripts/configure.sh` detects Docker or rootless Podman in Bash or zsh;
set `CLOUD_RUNTIME=podman` before sourcing to force Podman. Detection checks the runtime's
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

## Instance groups

On a disposable lab with the cloud running and the instance autoscaling module initially disabled, run the following from the checkout. The smoke creates real machines, generates traffic, stops the metrics service, verifies replacement of a failed member and checks that uninstall preserves independent resources.

```sh
bash tests/instance-autoscaling.sh
```

## Fresh rootless VM

On a host with Multipass, use the [VM provisioning script](podman-rootless.md#reproducible-multipass-vm) to build the committed checkout and test the full lifecycle, including guest reboot in ephemeral mode.
