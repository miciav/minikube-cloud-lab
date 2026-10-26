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

## 1. Prepare your Linux lab VM

Use the Linux VM provided or approved by your instructor. It needs:

| Requirement | Check |
| --- | --- |
| At least 6 CPUs and 12 GiB of available memory | `nproc` and `free -h` |
| Docker Engine running, with socket access for your user | `docker info` |
| Docker Compose v2 | `docker compose version` |
| `kubectl` v1.35 | `kubectl version --client` |
| Repository and private GHCR package access | Confirm with your instructor |

Use a dedicated lab VM: this exercise changes the current Kubernetes context
and allows the cloud service to create Docker resources. Keep the VM on a
trusted network; externalgrpc port 9090 is intended for the cluster's nodes.
The Compose instructions below target Linux. Native macOS development is
covered in the [README](../README.md#macos-with-docker-desktop).

Clone the repository, unless your instructor has already placed it at
`~/cloud-lab`:

```sh
git clone git@github.com:miciav/minikube-cloud-lab.git ~/cloud-lab
cd ~/cloud-lab
```

The clone command assumes GitHub SSH access is configured. Follow your
instructor's instructions for accessing the private repository and logging
Docker into `ghcr.io` before pulling images. Repository access and package
access must both be available.

Prepare the directories and Compose identity settings:

```sh
cd ~/cloud-lab
mkdir -p "$HOME/.minikube-cloud-lab" "$HOME/.kube"
printf 'UID=%s\nGID=%s\nDOCKER_GID=%s\n' \
  "$(id -u)" "$(id -g)" "$(stat -c '%g' /var/run/docker.sock)" > .env
docker compose --profile tools pull
```

The cloud writes its state under `~/.minikube-cloud-lab`. Both container tools
use the host's `~/.kube/config`. The IDs in `.env` let the cloud write those files
as your user and access the Docker socket through its group.

In your command terminal, define the two tool aliases and select the matching
host kubeconfig:

```sh
alias cloud='docker compose -f ~/cloud-lab/compose.yaml exec cloud cloud'
alias lab='docker compose -f ~/cloud-lab/compose.yaml run --rm lab'
export KUBECONFIG="$HOME/.kube/config"
```

Aliases and environment variables apply to the current terminal. In every new
terminal used below, run `cd ~/cloud-lab` and
`export KUBECONFIG="$HOME/.kube/config"`. Define the aliases there too if you
want to run `cloud` or `lab` in that terminal.

## 2. Start the cloud and request a cluster

Start the cloud service:

```sh
docker compose up -d cloud
docker compose ps
cloud machine-types list
```

If the last command reports a connection error, wait a few seconds and retry.
You should see `lab-small` with 2 CPUs and 4096 MiB, and `lab-medium` with
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
`host.minikube.internal:9090`. Autoscaler runs inside Kubernetes and contacts
the cloud provider through this endpoint. Creating a cluster and installing
Autoscaler are separate actions.

The `lab` command prints its equivalent `kubectl` command and applies the same
YAML files embedded from this repository. Record the equivalent command in
your lab notes.

## 4. Start observing, then create resource pressure

Keep your command terminal as **terminal A**. Open two observation terminals
before starting the workload.

In **terminal B**, watch the lab's Pods:

```sh
cd ~/cloud-lab
export KUBECONFIG="$HOME/.kube/config"
kubectl get pods -l app=lab-load -w
```

An initially empty list is expected. Leave the watch running.

In **terminal C**, watch Nodes:

```sh
cd ~/cloud-lab
export KUBECONFIG="$HOME/.kube/config"
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

On a clean lab, expect a control plane and one worker, commonly named
`demo-m02`. Machine names, operation IDs, and timestamps may vary.

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

**Checkpoint:** two Nodes are Ready, all four lab Pods are Running, and the cloud
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
| Private image pull is denied | Confirm Docker login and GHCR package access with your instructor |
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
