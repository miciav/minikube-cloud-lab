# Podman rootless

Use this guide to prepare a Linux VM for the class. For a rehearsal on a fresh Ubuntu guest, use Multipass. To configure an existing course image, follow the host preparation below. Students use the same shell commands for the exercises on either VM.

## Reproducible Multipass VM

From a checkout containing the changes to test, run:

```sh
bash scripts/multipass-rootless.sh
```

The host needs Git and Multipass. The script creates a dedicated Ubuntu 26.04
VM named `mcl-rootless-test` with 4 CPUs, 8 GiB of RAM and a 48 GiB virtual
disk. It provisions Podman, Compose, kernel modules and cgroup delegation,
then builds the cloud image from the indicated Git commit inside the VM.
Commit local edits before running. The VM needs internet access to Ubuntu
package mirrors, container registries and Go modules on its first run.

The short test creates two standalone instances and a blank disk, formats it
in an emulated Linux console, checks that detachment is refused while the
console is open and reads the file from the second instance. It also checks
SSH and ordinary instance persistence across stop/start. Use `--full` to also build the lab image and run
`verify-lab.py --runtime podman --no-pull` with its Kubernetes and storage
checks. A nonzero exit indicates a failed or incomplete verification.

Rerunning the command reuses the marked VM and its image cache. Specify
`--vm NAME` for a separate environment. The script leaves the VM available
for inspection. Stop it with `multipass stop mcl-rootless-test`.
For an isolated run use `--full --ephemeral --vm NAME` with an unused name.
The script connects with a one-time SSH key and purges its guest on exit,
including after a failed check.

## Prepare an existing Linux VM

The Linux VM needs systemd, cgroups v2 with CPU and memory controllers delegated to the student's user, subordinate UID/GID ranges, Podman and a Compose provider. Prepare these as the instructor; students run the lab without sudo. Load kernel modules during VM preparation and persist them across reboots:

```sh
modules=(br_netfilter vxlan iptable_nat iptable_filter iptable_mangle iptable_raw)
sudo modprobe -a "${modules[@]}"
printf '%s\n' "${modules[@]}" | sudo tee /etc/modules-load.d/cloud-lab.conf
```

Enable the user's API socket with `systemctl --user enable --now podman.socket`. For recovery after a guest reboot, enable `podman-restart.service` with `systemctl --user enable podman-restart.service` and enable lingering for the lab user. Verify `podman info` reports rootless and cgroups v2.

For annual acceptance, use the [course VM checklist](course-verification.md#annual-acceptance-on-the-it-provided-vm) before creating the example cluster below.

The CI builds and publishes the cloud and tools images to GHCR for amd64 and arm64 after checks pass on main. Once publication finishes, pull those images in the student VM:

```sh
source scripts/configure.sh
labcompose --profile tools pull
labcompose up -d cloud
cloud clusters create demo --min-nodes 1 --max-nodes 2
cloud clusters get-credentials demo
lab install autoscaler
lab load start
```

The helper supports Bash and zsh, detects rootless Podman and defines `labcompose`, `cloud`, `lab` and containerized `kubectl`. `labcompose` uses the standalone `podman-compose` command when installed, otherwise `podman compose`. No host kubectl installation is required. The wrapper uses the private cluster kubeconfig and mounts the current directory at `/workspace`, so `kubectl apply -f manifest.yaml` works normally. Read and write files relative to the current directory. Host kubeconfig export is optional. `kubectl port-forward pod/nginx 18080:80` starts a separate temporary tools container on the cluster network and publishes port 18080 on the VM's loopback address. It requires no host kubectl. Ctrl+C removes it. Choose an explicit free local port allowed by the rootless host (normally 1024 or above); random ports and non-loopback `--address` values are unsupported. For a remote VM, use an SSH tunnel to access its localhost from your laptop.

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

Ubuntu 26.04 instances (`ubuntu-26.04`, the default) were verified the same way. Following Docker's Ubuntu guide inside an instance installs a Docker Engine that runs `hello-world` and serves a published container. `apt install nginx` starts its service, and each Ubuntu instance has its own SSH host keys.

`systemctl is-system-running` reports `degraded` inside rootless instances: the kernel debug, tracing and configuration mounts and the binfmt automount cannot be mounted in a user namespace. Packages, services and Docker are unaffected. For a few seconds after a start, `systemctl` reports "Failed to connect to bus" while systemd and D-Bus come up. Run `RUNTIME=podman bash tests/instance-feasibility.sh` to check a new VM image before distributing it.

### On-demand instance autoscaling

The optional instance autoscaling profile uses the same `lab install` and
`lab uninstall` commands on Podman rootless. See the
[standalone HTTP exercise](tutorials/autoscaling/instance-groups.md)
for the workflow.

With the cloud service running on a disposable Podman lab, run the smoke test
from the checkout to verify scaling, health replacement, uninstall and reinstall:

```sh
CLOUD_RUNTIME=podman bash tests/instance-autoscaling.sh
```

## Existing VM without administrator privileges

Upgrading the operating system is not required just to install Compose. The configuration script installs `podman-compose` in your account automatically when no Compose provider is available. Python 3, pip and access to PyPI are required; no sudo is used:

```sh
systemctl --user enable --now podman.socket
source scripts/configure.sh
labcompose --profile tools pull
labcompose up -d cloud
```

Source `scripts/configure.sh` in each terminal; it adds `~/.local/bin` to PATH automatically without duplicating an existing entry. Podman Compose 1.x documents support for Podman 3.4 and later; the helper accepts both old and new cgroup info field names. This does not certify the complete lab on Podman 3.4.4: the full runtime test above used 6.1.4. Missing kernel modules, subordinate UID/GID mappings or cgroup delegation must still be prepared by the VM administrator. If pip is unavailable or user installation is blocked by the Python environment, setup stops with an error; ask for Python/pip or a Compose provider to be included in the VM image. Existing working providers are not reinstalled.

## Automated live verification

Run the [course verification](course-verification.md) before distributing the VM.

### Block-device feasibility on the test VM

From the checkout on the host, after provisioning the Multipass VM:

```sh
multipass exec mcl-rootless-test -- bash -s < tests/rootless-block-device.sh
```

The probe uses sudo in the dedicated VM to associate a fresh 64 MiB file with
a loop device and create a private device node for the lab user. Two successive
rootless Podman containers receive that node as `/dev/cloud-disks/dati`. The
first formats the block device using `mkfs.ext4` and writes a file through
`fuse2fs`. The second mounts the same disk and checks the file contents.
The probe removes its device and image on exit.

This optional probe demonstrates host-assisted loop allocation. Student disk
consoles use QEMU software emulation and can run without the probe.

### Student disk exercise with QEMU

In the VM, source `scripts/configure.sh` and start the rootless cloud. With two
running instances named `vm1` and `vm2`, run:

```sh
cloud disks create data --size 64Mi
cloud disks attach data --instance vm1
cloud disks shell vm1 data
```

The shell opens a minimal Linux guest with one virtio data disk. On first use
Podman builds the console image from this checkout. In that shell run:

```sh
lsblk
mkfs.ext4 /dev/vda
mount /dev/vda /mnt
echo hello > /mnt/message
umount /mnt
exit
```

Back on the VM, move the disk to the second instance:

```sh
cloud disks detach data --instance vm1
cloud disks attach data --instance vm2
cloud disks shell vm2 data
```

Run `mount /dev/vda /mnt` and `cat /mnt/message`. The guest root filesystem is
temporary; the disk image lives in the named Podman volume
`minikube-cloud-lab-rootless-disks`. The emulated console is separate from the
instance's ordinary SSH shell and shares the instance's lifetime. Exit the
console before stopping the instance, detaching the disk or deleting the owner.
Only one console can open a disk at a time. The host-side shell command is
provided by `scripts/configure.sh`; it runs QEMU with TCG, without KVM, sudo
or a privileged device helper.

## Reference environment

Verified on Fedora CoreOS 44 ARM64 with Podman 6.1.4 rootless, Kubernetes 1.35.6 and containerd 2.3.4, in a VM with 4 CPUs and 6 GiB RAM. The full autoscaling cycle 1 → 2 → 1 passed, with four load pods Running across two nodes. Physical CPU/RAM limits, HTTP from the host, the oversized-PVC warning and persistence after recreating a pod on its original node were also verified. Cross-node persistence is not an acceptance requirement.

All lab containers in this configuration run under Podman rootless. New Minikube nodes receive an explicit limit of 4096 processes/threads, preserved when CPU/RAM limits are updated. This avoids relying on implicit zero/unlimited PID handling in the Docker-compatible API. A node with `pids.max=1` cannot fork during entrypoint startup; verify the effective cgroup value, not only `podman inspect`.

This configuration uses real persistent directories shared by every node and a simulated allocation budget. It does not impose physical write quotas. Keep enough free disk space in the VM. Use dedicated rootless state; existing Docker/NFS volumes are not migrated.

## Optional course services

The [object storage, queue and database exercises](tutorials/README.md#managed-services) run as the course user without a Kubernetes cluster. Run `python3 scripts/verify-course-vm.py` before assigning them. The acceptance script needs all three modules to start disabled with no retained data. Each publishes its application endpoint on VM loopback; use a localhost SSH tunnel for access from a laptop.
