# Minikube Cloud Lab

Students can watch a Pending Pod trigger a request for a new machine, follow that request in the cloud operation history, and see the machine join Kubernetes. A second exercise scales a group of standalone HTTP servers in response to traffic. The same local cloud also supports labs on networking, storage and operators.

## Prepare the environment

Choose the runtime for the class before distributing the checkout. Each cloud manages one Kubernetes cluster at a time. The control plane counts toward the node limit. Standalone instance groups can run without a Kubernetes cluster.

| Classroom environment | Preparation | What changes for the lesson |
| --- | --- | --- |
| Linux with rootful Docker, or macOS with Docker Desktop | Give the default experiment at least 6 CPUs and 6 GiB of available memory. Students need Bash or zsh, Git, Compose v2 and access to the Docker socket. | Includes the web portal. The NFS backend enforces physical volume limits. [Docker host details](docs/operations.md#docker-host-and-credentials) |
| Linux VM with rootless Podman | Prepare the user socket, cgroups v2 delegation, kernel modules and a Compose provider before handing over the VM. The Multipass script provisions a guest with 4 vCPUs and 8 GiB RAM. | Students work without sudo on the host. Storage uses shared directories and an allocation budget. Standalone data disks use a QEMU console. [Prepare the VM](docs/podman-rootless.md) |

Give students a checkout and container images from the same course version. Published images follow successful CI on `main`. For an unpublished branch, build the images and select the local tags first. [Build instructions](docs/development.md#build-and-test)

From the checkout, start the cloud and check the machine catalog. Students can run these commands on either prepared runtime. Source the helper again in each new terminal.

```sh
source scripts/configure.sh
labcompose --profile tools pull
labcompose up -d cloud
cloud machine-types list
```

The pull step is for published images. With local image overrides, go straight from sourcing the helper to starting the cloud. Expect `lab-small` (2 CPUs, 2 GiB) and `lab-medium` (4 CPUs, 8 GiB). If the API is still starting, inspect `labcompose logs cloud` and retry the catalog command. On either runtime, run `lab install portal` and open `http://localhost:3000`. Install `monitoring` to record the lesson while the portal is closed.

## Verify before class

On the annual **IT-provided Linux VM**, install the course images and run the acceptance checks as the student account before creating any class resources:

```sh
python3 scripts/verify-course-vm.py
```

The runner includes portal and monitoring, managed services, instance provisioning and autoscaling, the mixed VM and Kubernetes capstone, and the Kubernetes exercises. It stops at the first failure and prints the location of its JSON report and per-stage logs. See the [course VM checklist](docs/course-verification.md#annual-acceptance-on-the-it-provided-vm) for prerequisites and coverage.

To verify just the portal and monitoring lifecycle on Docker or rootless Podman, run the smoke test on an empty lab with the course images available:

```sh
bash tests/monitoring.sh
```

It checks independent installation and removal, queue and bucket metrics, history retention after reinstall, and explicit history purge.

For the shorter Kubernetes check on a disposable lab with no existing cluster, run the acceptance suite for the runtime you will teach. Allow roughly 20–40 minutes. It creates a temporary cluster, grows it from one node to two, scales it back down, exercises networking and storage, then removes the cluster and stops the project. Exit code 0 means all checks passed. The last line gives the report location.

```sh
python3 scripts/verify-lab.py --runtime docker
```

Use `--runtime podman` inside a prepared VM. To rehearse the committed checkout from a Multipass host, use `bash scripts/multipass-rootless.sh --full --ephemeral --vm course-rehearsal` with an unused guest name. That run also tests instance groups and recovery after a guest reboot. For an existing cluster or a targeted instance-group rehearsal, see the [verification guide](docs/course-verification.md).

## Choose the lesson

Use the [tutorials by topic](docs/tutorials/README.md) to select an exercise or share the catalog with students. The table below lists the environment to prepare for each lesson.

| Learning objective | Assign to students | Starting point |
| --- | --- | --- |
| Explain why a Pending Pod causes a worker to be added | [From Pod to cloud machine](docs/tutorials/autoscaling/kubernetes-nodes.md) | Running cloud, room for two small nodes. The tutorial creates its own cluster. |
| Compare NodePort and LoadBalancer endpoints | [Expose Nginx through three Services](docs/tutorials/networking/service-exposure.md) | Running cluster. The exercise uses its own namespace and works with YAKD removed. |
| Follow a PVC through allocation, release and recovery | [Persistent storage](docs/tutorials/storage/persistent-volumes.md) | Dedicated lab. The recovery exercise deletes and recreates a cluster. |
| Bootstrap a Linux instance and move a persistent data disk | [Standalone instances and disks](docs/tutorials/compute/instances-and-disks.md) | Running cloud. Choose the disk procedure for your runtime. |
| Observe traffic-driven scaling, health checks and replacement | [Autoscale an HTTP service](docs/tutorials/autoscaling/instance-groups.md) | Running cloud, room for three instances. |
| Deploy an application across a VM and Kubernetes | [OpenTofu, cloud-init, Ansible and kubectl](lab/capstone/README.md) | Dedicated cloud, bundled lab tools, Ubuntu image with cloud-init. |
| Teach the Kubernetes exercise decks | [Lecture 02](kubernetes/lecture02/README.md) · [Lecture 03](kubernetes/lecture03/README.md) | Use the node counts and machine sizes in the selected lesson. Lecture 03 includes HPA, VPA, Cluster Autoscaler, KEDA and operators. |

For the handout, give students the checkout version, the host or VM to use, the assigned guide and the expected starting state. For the Pod-to-machine lesson, ask for the scheduling event and the cloud operation ID that explain why the worker appeared. The [tutorial report](docs/tutorials/autoscaling/kubernetes-nodes.md#lab-report) lists the evidence to collect before cleanup.

## Optional modules

Use `lab install NAME`, `lab status NAME` and `lab uninstall NAME` from a configured shell.

| Name | Scope and initial state | Effect of uninstall |
| --- | --- | --- |
| `autoscaler` | Kubernetes cluster. Install before the Pod scheduling exercise. | Removes the controller and its configuration. Existing nodes and workloads remain. |
| `instance-autoscaling` | Shared by instance groups in the lab. Install to start Traefik, Prometheus and Nomad Autoscaler. | Drains and deletes **all groups, their machines and local filesystems**. Independently created instances and persistent volumes survive. |
| `portal` | Local web console on port 3000. | Releases the port. Monitoring can continue independently. |
| `monitoring` | Up to 24 hours of cluster, queue and bucket history. | Stops sampling. Reinstall to reopen retained history, or purge the disabled module to erase it. |
| `yakd` | Kubernetes dashboard. Included when a new cluster is created. | Removes the managed dashboard resources and retains its namespace for student resources. |

The [managed services exercises](docs/tutorials/README.md#managed-services) use three independent modules: `object-storage` (Garage), `queue` (ElasticMQ) and `database` (PostgreSQL). Install any one from the configured shell without starting Kubernetes. Uninstall releases its containers and ports while retaining its data. See the [command reference](docs/commands.md#managed-services).

## Run and maintain the lab

- [Command reference](docs/commands.md) for cluster sizing, instances, groups, modules and operation history.
- [Operations guide](docs/operations.md) for credentials, the portal, networking, storage and reset. Finish resource cleanup through the cloud before stopping Compose.
- [Development guide](docs/development.md) for the architecture, image builds, test commands and CI.
