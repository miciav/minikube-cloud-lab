# Kubernetes_5 — lecture03

Autoscaling and Operator pattern exercises for minikube-cloud-lab. Provider access follows `Kubernetes_4.pptx`. For HPA, VPA, CRD and MySQL, the instructor verified **two lab-medium nodes, one control plane and one worker**, with workloads in `default`. Run one exercise at a time and remove its objects before starting the next.

**Verified November 11, 2026.** HPA scaled 1 → 6 → 1. The CRD was created and removed. Three MySQL members reached ONLINE and the SQL connection succeeded. Later, Cluster Autoscaler scaled 1 → 3 → 1 nodes and KEDA scaled 0 → 2 → 0 replicas. Cleanup was verified between exercises. [Results and limitations](SESSION.md).

The [Cluster Autoscaler exercise](CLUSTER_AUTOSCALING.md) uses a lab-small cluster with one to three nodes and manifests in `lecture03/autoscaler/` and `lecture03/load.yaml`. Students apply resources with `kubectl apply` and set replicas to 0 → 4 → 0 with `kubectl scale`. Nodes scale automatically. The manual run left one Ready lab-small node, a cloud range of 1–3 and no workload or autoscaler. After cleanup, restore the lab-medium setup before MySQL. The later automated verification removed the cluster and volumes as requested. [Session log](SESSION.md).

The [KEDA Cron scaler exercise](keda/README.md) follows Cluster Autoscaler and reuses the cleaned node for a time window with **0 → 2 → 0 replicas**. It includes two teaching manifests and the official KEDA 2.21.0 release under `keda/upstream/`. Update the UTC times before each run.

## Setup HPA / VPA / CRD / MySQL

Run these commands from the repository root in each terminal.

```sh
source scripts/configure.sh
docker compose up -d
cloud clusters list
cloud machine-types list
```

If no cluster exists, create one sized for these experiments.

```sh
cloud clusters create lecture03 --machine-type lab-medium --min-nodes 2 --max-nodes 2
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes -o wide
cloud machines list --cluster lecture03
```

If a suitable course cluster already exists, substitute its name. The provider manages one cluster. Preserve existing data and clusters. The `resize` command changes the node count and retains the machine type.

Each lab-medium node has a limit of 4 CPUs and 8 GiB. Allow for 8 CPUs and 16 GiB across both nodes plus the supporting services. Limits do not reserve physical memory. Check capacity and actual usage with `docker stats --no-stream`, especially on a host with about 16 GiB. Both nodes share the same host failure domain.

The Kubernetes_4 slides start with one lab-small node and grow to three. In the first run, HPA and MySQL overlapped on three lab-small nodes, the control plane hit its 2 GiB memory limit and the API timed out. The instructor approved larger nodes and sequential exercises. See `SESSION.md` for details. MySQL alone on three lab-small nodes was not verified.

```sh
kubectl config current-context
kubectl config view --minify -o 'jsonpath={..namespace}'
kubectl get pods,deployments,statefulsets,services,hpa,pvc -A
kubectl get crd
kubectl get storageclass
cloud storage status
cd kubernetes/lecture03
```

Check the context, namespace (`default` or empty), capacity and exercise object names. Install CRDs and operators in the indicated order. Applying the whole folder with `kubectl apply -f .` bypasses that order. Follow `commands.txt` manually and stop at its checkpoints.

## Exercises

| Exercise | Files | Success criterion |
| --- | --- | --- |
| HPA | `php-apache.yml`, `hpa.yml` | Replicas increase under load and return to one |
| VPA | `vpa/` | Off produces recommendations. Recreate replaces Pods with higher CPU requests and retains two replicas. |
| Cluster Autoscaler | `CLUSTER_AUTOSCALING.md`, `autoscaler/`, `load.yaml` | CPU shortage leaves Pods Pending, nodes join, four replicas become available, then the cluster returns to one node |
| KEDA Cron scaler | `keda/` | Replicas follow a UTC window, 0 → 2 → 0, with an HPA created by KEDA |
| CRD | `crontab-crd.yml`, `crontab-example.yml` | Register a type, then create, read and delete an object |
| MySQL Operator | `mysql-operator-26.10.0-2.3.1/`, `mycluster.yml` | Three ONLINE members and a SQL connection through Router |

VPA follows HPA. Cluster Autoscaler and KEDA precede the Kubernetes extension exercises. The hidden PostgreSQL slides are outside this lesson. References use slide titles so they still work when instructors insert slides.

### VPA

Follow the [VPA guide](vpa/README.md) on the two lab-medium nodes after HPA cleanup. VPA 1.7.1 observes two CPU-consuming Pods, recommends resources in `Off` mode and replaces the Pods in `Recreate` mode. It controls CPU requests only. **Observed result:** both replacement Pods went from 50m to 500m with the same limits, memory, replica count and node count. Cleanup and cluster deletion succeeded. The upstream manifests and TLS generator are included.

### HPA

The provider installs Metrics Server. Check its Deployment, API and `kubectl top nodes`. Leave the host's personal minikube profile alone. Generate the slide's HTTP load with `busybox:1.28` and stop it with Ctrl+C in the same terminal.

The CPU target is 50% of the 200m request, about 100m per Pod. The CPU limit is 500m. The HPA allows 1–10 replicas. Initial metrics can show `<unknown>` before the first collection. Wait for `ScalingActive=True` before applying load.

The manifest sets a 60-second scale-down stabilization window. Wait for one replica without editing the Deployment. Timing also depends on metrics. [How HPA works](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/).

### CRD

The CRD adds `CronTab` to the API. Its objects store `cronSpec` and `image`. Unlike the built-in CronJob, this exercise has no controller to act on those fields or start processes. Wait for `Established` before creating the instance. Deleting the CRD deletes its instances. Only delete a CRD created for this run after checking that nobody else owns an instance. [Kubernetes example](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/).

### MySQL Operator

The local upstream manifests use tag `26.10.0-2.3.1`, updated at the instructor's request. The default Server and Router version is `26.10.0`. MySQL Shell also uses the pinned tag.

The operator reconciles StatefulSets, Pods, Services and PVCs from the InnoDBCluster object. The original manifest requests three MySQL instances and one Router. With two nodes, two instances can share a node, so replication alone does not isolate node failures. Check the default `cloud-standard` StorageClass and allow 6 GiB for three 2 GiB PVCs before applying the manifest.

The password `cloud` is the public example from the slides, for the isolated lab only. Use `kubectl create secret`. The materials contain no private credentials or kubeconfig. MySQL certificates are self-signed as in the original example.

Check the actual domain in CoreDNS. This operator tag detects it automatically. The recorded run used `cluster.local`, but provider versions can differ. Wait for ONLINE with three members, then verify connectivity and membership with `verify-mysql.sql`.

Sources include [installation](https://dev.mysql.com/doc/mysql-operator/en/mysql-operator-installation-kubectl.html), [InnoDB Cluster](https://dev.mysql.com/doc/mysql-operator/en/mysql-operator-innodbcluster-simple-kubectl.html) and [DNS detection in this tag](https://github.com/mysql/mysql-operator/blob/26.10.0-2.3.1/mysqloperator/controller/kubeutils.py).

## Cleanup and results

Before each exercise, check for leftover Pods, Deployments, HPAs, custom objects and volumes. Delete only objects created for the test. Remove the MySQL custom object while the operator is active, then check remaining PVCs and cloud volumes before uninstalling the operator. Follow `commands.txt`. Preserve the default namespace, other users' volumes and the course cluster.

`SESSION.md` records observations, adaptations and limitations. Local logs are under the Git-ignored `evidence/` directory. HPA and MySQL manifests come from the original `Cloud 2025 lectures - see Drive/Esercitazioni/K8S-Lecture04` materials. `SHA256SUMS` records the distributed manifests.


## Run the deck automatically

From the repository root, with cloud services running:

```sh
source scripts/configure.sh
labcompose up -d
python3 scripts/verify-lecture03.py --recreate-empty-cluster
# To select a runtime explicitly, add --runtime docker or --runtime podman.
```

This runner is separate from `scripts/verify-lab.py` and uses the Python standard library. It requires the **dedicated, disposable lecture03 cluster**. The flag `--recreate-empty-cluster` authorizes deleting and recreating the empty cluster between node configurations. Run it while you have exclusive use of the lab. The provider must already be running and the runner leaves Compose services active.

Before modifying the cluster, the runner checks manifest checksums, namespaces, default workloads and resources, controllers in other namespaces, CRDs, PVs, cloud volumes and existing autoscaler or adapter installations. It stops on name collisions, unrelated resources or active cloud operations and preserves existing data.

The runner follows the deck order.

1. **HPA**, two lab-medium nodes. Observe scale-up from one available replica and a return to one after removing the generator and waiting for the configured 60-second stabilization.
2. **VPA**, same nodes. Off recommends changes without modifying Pods. Recreate yields two new UIDs and higher CPU requests with the same memory and limits.
3. **Cluster Autoscaler**, lab-small min 1 / max 3. Apply the local files, scale 0 → 4 → 0, observe Pods Pending for CPU and at least two Ready nodes. Confirm the run's ADD_MACHINE and DELETE_MACHINE operations reach DONE.
4. **KEDA**, same cleaned node. Observe the UTC window, 0 → 2 → 0 replicas, an HPA owned by the ScaledObject and the HTTP response `Hello, KEDA!`. The runner updates times in a temporary copy, including across UTC midnight, preserving the repository file and checksum.
5. **CRD**, same node. Register CronTab, verify the spec and clean up.
6. **MySQL Operator**, two lab-medium nodes. Verify three ONLINE members, Router availability, SQL through Router and release of PVCs, PVs and cloud volumes.

The runner cleans up each exercise before starting the next. InnoDBCluster uses `background` deletion so the operator can read its Secrets until finalizers finish. Other manifests, including the Cluster Autoscaler Deployment, use `foreground`. At the end the runner **deletes the lecture03 cluster** and checks for remaining clusters and volumes. Cloud Compose services stay up. Allow 40–75 minutes, image registry access and host capacity for two nodes limited to 4 CPUs / 8 GiB each. Actual usage may be lower. `--timeout SECONDS` changes the convergence wait (default 900). Individual calls and rollouts have their own limits.

The runner prints the log directory at startup. It contains `commands.log`, JSON snapshots, a temporary KEDA manifest, SQL output and `report.json` with PASS/FAIL. Exit 0 means all checks and cleanup passed, 1 means failure or interruption, and 2 means invalid CLI arguments. On failure it collects diagnostics, attempts cleanup of the active exercise and stops with the remaining state available for inspection. Consult the instructor with the report and logs. Do not force finalizers or delete volumes to make a test pass. SIGINT/SIGTERM during an exercise triggers cleanup. Interrupting provisioning can leave a cloud operation running.

Run the same runtime-independent tests as CI with:

```sh
python3 -m unittest discover -s tests -p 'test_verify*.py'
```
