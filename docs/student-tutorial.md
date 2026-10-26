# Student tutorial: follow an autoscaling request from Pod to machine

[Project overview and command reference](../README.md)

In this lab you will ask a simulated cloud for a Kubernetes cluster, install
Cluster Autoscaler, and watch Kubernetes request an additional machine when a
workload cannot fit. You will then remove the workload and observe the cloud
release the extra machine.

## What you will learn

By the end, you should be able to:

- Distinguish a cloud machine from its registered Kubernetes Node.
- Explain the responsibilities of the scheduler, Cluster Autoscaler, and cloud.
- Connect a Pending Pod to a resource request and a cloud operation.
- Explain why resource requests can trigger scale-up even when CPU usage is low.
- Verify scale-down and clean up the cluster without losing cloud state.

You should already be familiar with a terminal and the Kubernetes concepts of
Pod, Deployment, and Node. No Go programming is required.

## 1. Prepare Docker on your lab host

Use the Linux VM or Mac approved by your instructor. It needs:

| Requirement | Check |
| --- | --- |
| At least 6 CPUs and 6 GiB of available memory | Linux: `nproc` and `free -h`; Mac: Docker Desktop resource settings |
| Docker Engine (Linux) or Docker Desktop (Mac) running, with local socket access | `docker info` |
| Docker Compose v2 | `docker compose version` |
| `kubectl` v1.35 | `kubectl version --client` |
| Internet access to GitHub and GHCR | The VM can reach `github.com` and `ghcr.io` |

The default two-node experiment uses machines capped at 2 GiB each (4 GiB
combined); leave the remaining memory budget for the VM and cloud service.

Use a dedicated lab environment: this exercise changes the current Kubernetes context
and allows the cloud service to create Docker resources. Keep the VM on a
trusted network. Compose keeps externalgrpc port 9090 inside a dedicated Docker
bridge and publishes the management API only on localhost. These instructions
work on Linux and Mac; Docker Desktop host networking is not required.
The project commands run in containers, so no native `cloud`/`lab` binaries or
Go installation are needed. `kubectl` is the standard host Kubernetes client.

Clone the repository, unless your instructor has already placed it at
`~/cloud-lab`:

```sh
git clone https://github.com/miciav/minikube-cloud-lab.git ~/cloud-lab
cd ~/cloud-lab
```

The repository and container images are public. The HTTPS clone command needs
no GitHub account or SSH key, and pulling the images needs no Docker login to
`ghcr.io`.

Prepare the configuration in your **Bash or zsh** terminal:

```sh
cd ~/cloud-lab
source scripts/configure.sh
```

This creates the data directories, adds missing UID/GID/socket-group values to
`.env`, defines the `cloud` and `lab` command wrappers, and selects the matching
host kubeconfig for this session. It leaves existing `.env` values unchanged,
including image overrides. If a copied `.env` contains IDs belonging to a
different user, correct them before continuing.

The cloud stores state and its private kubeconfig under `~/.minikube-cloud-lab`.
The `lab` container reads that state mount read-only to reach Kubernetes on the
bridge network. Host `kubectl` uses `~/.kube/config`, where `cloud` exports the
localhost API endpoint. The IDs let the tools access their files as your user
and let the cloud access the local Docker socket. On Mac, the helper configures
group 0 for Docker Desktop's VM socket.

The script only configures files and the current shell. You will explicitly
pull the images and start the cloud with Compose. It does not create a cluster,
install Autoscaler, or start the workload, and it does not edit `.bashrc`, `.zshrc`, or
other shell startup files.

In **every new Bash or zsh terminal** used for the lab, run:

```sh
source ~/cloud-lab/scripts/configure.sh
```

The wrappers work from other directories too. Use `source`, rather than
executing the script: a child process cannot configure its parent terminal.

## 2. Start the cloud and request a cluster

Pull the images, then start the cloud service yourself:

```sh
cd ~/cloud-lab
docker compose --profile tools pull
docker compose up -d cloud
docker compose ps
cloud machine-types list
```

If the last command reports a connection error, wait a few seconds and retry.
You should see `lab-small` with 2 CPUs and 2048 MiB, and `lab-medium` with
4 CPUs and 8192 MiB. To diagnose startup, use `docker compose logs cloud`.

**Predict:** when you request a cluster with minimum 1 and maximum 3, how many
machines should exist immediately? Does the control plane count?

```sh
cloud clusters list
cloud clusters create demo --machine-type lab-small --min-nodes 1 --max-nodes 3
cloud clusters describe demo
cloud machines list --cluster demo
cloud operations list
```

New `lab-small` machines have 2 GiB of memory. A cluster created with an older
4 GiB shape keeps its original limits: delete it through the cloud and recreate
it with the updated images to use the smaller shape.

The initial cluster list should be empty. If another cluster exists, finish or
clean up that earlier exercise first: this cloud manages one cluster at a time.
The first creation may take several minutes while images and Kubernetes
components are downloaded.

**Checkpoint:** creation finishes successfully, the cloud records one machine
named `demo` with role `control-plane`, and the CREATE_CLUSTER operation has
requester `student` and status DONE. The maximum is a limit; it does not cause
three machines to be provisioned immediately.

Now obtain Kubernetes credentials:

```sh
cloud clusters get-credentials demo
kubectl config current-context
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes -L node.kubernetes.io/instance-type,topology.kubernetes.io/zone
```

The current context should be `demo`. Expect one Ready Node with instance type
`lab-small` and zone `lab-region-1-a`.

A **machine** belongs to the cloud. A **Node** is Kubernetes' representation of
a registered machine. Creation and registration happen at different times, so
their lists can temporarily differ during an operation.

Inspect the scheduling budget:

```sh
kubectl describe node demo
```

Find **Allocatable** and **Allocated resources**. Allocatable CPU should be 2;
system Pods already consume part of that budget through their requests.
The backend shares the host kernel, so the raw Capacity field may reflect the
host. Use Allocatable when reasoning about scheduling this experiment.

### Open the dashboard installed by the cloud

The cloud automatically installs the YAKD addon in new clusters. No separate
install command is needed. Confirm that it is ready:

```sh
kubectl -n yakd-dashboard rollout status deployment/yakd-dashboard --timeout=5m
kubectl -n yakd-dashboard get pods,service
```

In another configured terminal, keep this command running:

```sh
kubectl -n yakd-dashboard port-forward --address 127.0.0.1 service/yakd-dashboard 8081:80
```

Open <http://127.0.0.1:8081> on the machine running `kubectl`. For a remote lab
VM, run `ssh -L 8081:127.0.0.1:8081 USER@VM` on your computer and open that URL
locally. Replace `USER@VM` with your lab login. The dashboard can administer the
entire cluster; the upstream addon also has a NodePort Service. Keep the lab
network trusted and do not expose the UI publicly.

Use YAKD to inspect Nodes, Deployments, Pods and events while following the
experiment below. It requests 128 MiB of the scheduling budget. Live CPU/memory
usage graphs require metrics-server, which this lab does not install.
Ctrl-C stops port forwarding; YAKD stays installed until the cluster is deleted.

## 3. Install Cluster Autoscaler before generating load

You install Autoscaler as a Kubernetes workload using the lab tools:

```sh
lab install autoscaler
kubectl -n kube-system rollout status deployment/cluster-autoscaler --timeout=5m
kubectl -n kube-system get deployment cluster-autoscaler
```

**Checkpoint:** the rollout completes and the Deployment has one available
replica. Complete this checkpoint before starting the load.

Inspect the configuration files:

- [Autoscaler Deployment](../lab/manifests/autoscaler/deployment.yaml)
- [Cloud connection configuration](../lab/manifests/autoscaler/cloud-config.yaml)
- [Autoscaler permissions](../lab/manifests/autoscaler/rbac.yaml)

Find `--cloud-provider=externalgrpc` and the address
`host.minikube.internal:9090`. Nodes and CoreDNS map this name to the cloud
container on the shared Docker bridge. Autoscaler runs inside Kubernetes and
contacts the cloud through this internal endpoint. Creating a cluster and installing
Autoscaler are separate actions.

The `lab` command prints its equivalent `kubectl` command and applies the same
YAML files embedded from this repository. Record the equivalent command in
your lab notes.

## 4. Start observing, then create resource pressure

Keep your command terminal as **terminal A**. Open two observation terminals
before starting the workload.

In **terminal B**, watch the lab's Pods:

```sh
source ~/cloud-lab/scripts/configure.sh
kubectl get pods -l app=lab-load -w
```

An initially empty list is expected. Leave the watch running.

In **terminal C**, watch Nodes:

```sh
source ~/cloud-lab/scripts/configure.sh
kubectl get nodes -w
```

These watches continue until you press Ctrl-C. Ctrl-C stops observation; it
does not delete Pods, Nodes, or the cluster.

Back in **terminal A**, read the [load manifest](../lab/manifests/load.yaml).
It defines four Pods, each requesting `600m` CPU and `64Mi` memory.

**Predict:** calculate the total CPU request. Can it fit within 2 allocatable
CPUs, alongside the system Pods?

```sh
lab load start
```

Watch terminals B and C. Some Pods should initially remain Pending while the
cloud still has one machine. A worker Node then appears, becomes Ready, and
allows the remaining Pods to run. Pending can be brief, so keep the watches
running from before the load starts.

Use terminal A to inspect what happened:

```sh
kubectl get events --field-selector reason=FailedScheduling --sort-by=.lastTimestamp
kubectl get pods -l app=lab-load
kubectl -n kube-system logs deployment/cluster-autoscaler --since=5m
cloud operations list
cloud machines list --cluster demo
```

While a Pod is Pending, `kubectl describe pod POD_NAME` shows its scheduling
events. Replace `POD_NAME` with a name from the Pod list. Look for an insufficient
CPU scheduling reason. Events help reconstruct the sequence if scale-up was too
quick to observe live; exact messages and timings can vary.

**Important:** the `pause` containers generate scheduling pressure through
**resource requests**. They do not deliberately burn 600m of CPU. Cluster
Autoscaler reacts to Pods that cannot be scheduled; high measured CPU usage
is not required for this experiment.

## 5. Connect the Kubernetes decision to the cloud request

On a clean lab, the workload usually needs a control plane and one worker,
commonly named `demo-m02`. Autoscaler can request a second worker while a new
node or its Pods are still settling, up to the configured three-machine maximum.
Machine names, operation IDs, and timestamps may vary.

Wait for all four replicas to become available:

```sh
kubectl rollout status deployment/lab-load --timeout=5m
kubectl get pods -l app=lab-load -o wide
kubectl get nodes -L node.kubernetes.io/instance-type
cloud machines list --cluster demo
cloud operations list
```

If the new node is still provisioning, continue observing; allow up to
15 minutes for the initial scale-up before using the troubleshooting section.

**Checkpoint:** at least two Nodes are Ready, all four lab Pods are Running, and the cloud
has an ADD_MACHINE operation with requester `cluster-autoscaler` and status DONE.
The worker's `CREATED_BY` column should also identify `cluster-autoscaler`.
Use `cloud operations describe OPERATION_ID` to inspect that operation.

Complete this evidence table in your notes:

| Link in the chain | Evidence to record |
| --- | --- |
| Workload requests capacity | Replica count and per-Pod CPU request from YAML |
| Scheduler cannot place all Pods | Pending observation or FailedScheduling event |
| Autoscaler decides to scale up | Relevant Autoscaler log entry |
| Autoscaler asks the cloud | ADD_MACHINE operation ID and requester |
| Cloud provisions a machine | Machine name, status, and creator |
| Machine registers as a Node | Node name and Ready transition |
| Capacity becomes usable | Four available replicas and their assigned Nodes |

The responsibilities are distinct:

| Component | Responsibility in this experiment |
| --- | --- |
| Deployment controller | Maintains the requested four Pod replicas |
| Kubernetes scheduler | Places Pods where their requests fit |
| Cluster Autoscaler | Requests a change to the cloud's machine count |
| Cloud provider | Provisions and removes machines, recording operations |

The Deployment still has four replicas after node scale-up. Verify its desired
replica count with `kubectl get deployment lab-load`.

## 6. Remove the workload and observe scale-down

**Predict:** after the workload is removed, what prevents the cluster from
shrinking to zero machines?

In terminal A:

```sh
lab load stop
```

Continue watching Nodes in terminal C. Autoscaler waits for its configured
scale-down delays before requesting removal of the worker. The manifests set
one-minute delays for this lab, but the whole sequence can take several minutes.

```sh
cloud operations list
cloud machines list --cluster demo
kubectl get nodes
```

**Checkpoint:** the cloud records DELETE_MACHINE with requester
`cluster-autoscaler` and status DONE, and only the control-plane machine remains.
The minimum is one and the control plane is protected.

A Kubernetes Node can disappear **before** Docker teardown finishes. Wait until
`cloud operations list` shows no RUNNING operation before deleting the cluster.
If removal reports ERROR, inspect that operation rather than assuming the
machine has been fully removed.

## 7. Optional experiment: reach the maximum size

Complete the previous scale-down checkpoint before starting this experiment.
This experiment targets the three-machine maximum. A third 2 GiB machine can
also appear transiently during the earlier scale-up; allow that headroom when
planning the lab. To enforce a two-machine budget, create the cluster with
`--max-nodes 2` in section 2 and skip this optional experiment.
Check the maximum and request ten replicas:

```sh
cloud clusters describe demo
lab load start --replicas 10
```

**Predict:** ten Pods request 6 CPUs in total. The cluster is limited to three
machines, each with 2 allocatable CPUs, and system workloads also need resources.
Can all ten Pods run?

Observe the same Pod, Node, Autoscaler, and cloud-operation views. Expect the
cloud to reach its size limit while some Pods remain Pending. Do not wait for
all ten replicas to become available: the resource budget cannot fit them all.
The maximum includes the control plane.

Remove this workload and repeat the scale-down checkpoint:

```sh
lab load stop
```

Allow time for both workers to be removed: this lab permits one machine
removal at a time. Finish with one Ready control plane and all cloud operations
in a terminal state.

## 8. Clean up through the cloud

In terminal A, once no operation is RUNNING:

```sh
lab uninstall autoscaler
cloud clusters delete demo
cloud clusters list
cloud machines list
cloud operations list
docker compose down
```

**Checkpoint:** the cluster and machine lists are empty, and DELETE_CLUSTER is
DONE with requester `student`. The operation history remains on disk.
Stop the observation watches with Ctrl-C. API connection errors after deleting
the Kubernetes cluster are expected.

`docker compose down` stops the cloud service; it does not itself delete the
machines owned by that service. Always complete `cloud clusters delete` first.
The `demo` kubeconfig context can remain after deletion. Other contexts are
preserved; deleting all of `~/.kube` is unnecessary.

For an optional full reset of the operation history, see
[State and reset](../README.md#state-and-reset). Delete the state directory only
after cluster deletion has succeeded, so the cloud retains the information
needed to clean up its resources.

## Troubleshooting

| Observation | What to inspect |
| --- | --- |
| An image pull fails | Check the image name/tag and network access to `ghcr.io`; the default images are public and need no login |
| Docker socket permission is denied | Check `docker info`, the socket's group, and `.env`; ask your instructor to correct access |
| The cloud CLI cannot connect | Run `docker compose ps` and `docker compose logs cloud`; check the service has started |
| `kubectl` uses the wrong cluster | Check `KUBECONFIG` and `kubectl config current-context`; repeat `cloud clusters get-credentials demo` |
| Another cluster already exists | List it and finish or clean up that earlier exercise before creating another |
| Pods are Pending and no machine is added | Check the Pod's scheduling event, Autoscaler rollout/logs, size limits, and cloud operations |
| A Node is Ready but a Pod stays ContainerCreating or ImagePullBackOff | Inspect that Pod's events; a new machine alone does not resolve image or network errors |
| Scale-down has not happened yet | Check remaining workloads, configured delays, and Autoscaler logs; allow several minutes |
| Cluster deletion says an operation is still running | Wait for its terminal status and inspect its operation ID |
| Cluster or operation is ERROR | Inspect `cloud operations describe ID` and cloud logs; restore Docker availability before retrying cleanup |

Keep cloud state while diagnosing errors. A machine may have been partially
created before an operation failed. Removing the state files or deleting Docker
resources outside the cloud can hide the information needed for recovery.

## Lab report

Submit your evidence table and brief answers to these questions:

1. Who created the first machine, and who requested the worker?
2. How is a cloud machine different from a Kubernetes Node during provisioning?
3. Why did the workload need more capacity even though its containers were mostly idle?
4. Did node scale-up change the Deployment's desired replica count?
5. Which operation proves that the scale-down request came from Autoscaler?
6. Why must machine teardown finish before deleting the cluster?
7. If you completed the optional experiment, why did Pending Pods remain at the maximum size?

Use operation IDs, timestamps, scheduling events, and Pod/Node observations to
support your answers.
