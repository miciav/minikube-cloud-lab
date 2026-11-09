# Kubernetes_3 — lecture02

Companion exercises for Kubernetes_3. All exercise workloads run in namespace
`default`. `lecture02` remains the directory, example cluster name and lab label;
it is not an exercise namespace. No Namespace manifests are needed.

## Setup and working directory

Use the existing [minikube-cloud-lab](https://github.com/miciav/minikube-cloud-lab)
checkout with Docker Desktop / Docker Engine and Compose v2. Cross-worker
networking and drain require a control plane plus two workers. Start exercises
1–4 with one node and add the two workers immediately before exercise 5.
A lab-small node needs 2 CPUs / 2 GiB, plus supporting services; later allow
6 CPUs / 6 GiB for the three nodes, plus supporting services.
Do not delete an existing cluster or its volumes to run this material.

From the project root, in every Bash or zsh terminal:

```sh
source scripts/configure.sh
cloud clusters list
```

If no cluster exists, create a dedicated course cluster:

```sh
cloud clusters create lecture02 --machine-type lab-small --min-nodes 1 --max-nodes 1
cloud clusters get-credentials lecture02
```

When reusing a suitable course cluster, use its real name for get-credentials.
Inspect the context and nodes before running exercises:

```sh
kubectl config current-context
kubectl get nodes -o wide
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl config view --minify -o 'jsonpath={..namespace}'
cd kubernetes/lecture02
```

The namespace check must print `default`; an empty value also means `default`.
`get-credentials` configures this namespace. The wrapper mounts kubeconfig read-only,
so do not use `kubectl config set-context` inside it.

All subsequent file paths are relative to `kubernetes/lecture02`. `kubectl` is a
shell function configured by the provider and mounts the current directory.
Use `kubectl get ... -w`, not `watch kubectl`, and source `ex03/prepare.sh` in the
configured shell rather than launching it as a new process.

The current provider includes its NFS compatibility correction. Use the
provider README for startup, image versions and storage maintenance; do not
apply historical patches to an updated checkout.

## Before exercise 5: add workers

Complete exercises 1–4 on the initial node. Before exercise 5, keep hello-go and
web, return from `kubernetes/lecture02` to the project root, and add workers:

```sh
cd ../..
source scripts/configure.sh
cloud clusters resize lecture02 --nodes 3
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes -o wide
cd kubernetes/lecture02
```

Replace `lecture02` with the actual name (for example `deck3`). The command sets min=max=3 and preserves
cluster identity, workloads and storage; shrinking is rejected. Workers inherit
the current machine type. Three lab-medium nodes have aggregate limits of
12 CPUs / 24 GiB: account for the Docker host's actual capacity.
If `cloud clusters resize --help` is unavailable, return to the project root.
Select a published cloud image containing resize in `.env`, pull it with
`docker compose pull cloud`, and run `docker compose up -d --no-deps cloud`.
The default image is `ghcr.io/miciav/minikube-cloud-lab/cloud:latest`. Verify
`cloud clusters resize --help` before continuing. To build an updated checkout
locally instead, run `docker build --target cloud -t minikube-cloud-lab/cloud:dev .`,
set `CLOUD_IMAGE=minikube-cloud-lab/cloud:dev` in `.env`, and recreate only cloud.

Checkpoint: three Ready nodes, including two workers. Continue with exercise 5.
Keep these workers for cross-worker networking and the later drain exercise;
there is no intermediate shrink step. Existing workloads remain available.

## Exercise files

| Exercise | Files / workload | Result to check |
| --- | --- | --- |
| 1 | hello-go, imperative commands | Four Ready Pods and ready Service endpoints |
| 2 | `02-web.yaml` | Three Nginx replicas and an HTTP page |
| 3 | web Pod replacement | Different Pod UID, three Ready replicas restored |
| 4 | web scaling; optional `04-hello.yaml` | Six replicas, unchanged node count, hello logs |
| 5 | `ex03/prepare.sh`, `constraints.yaml` | Three constrained Pods on the selected worker |
| 6 | `ex03/network.yaml`, `02-web-node.yaml` | Different worker nodes, PONG/OK/k8s, usable host endpoint |
| 7 | Student-authored client/Nginx manifests | Three replicas each, successful `/get-backend` |
| 8 | hello-go update and rollback | Earlier Pod template restored |
| 9 | `00_es01_nginx_deployment.yml`, `00_es02_nginx_deployment.yml` | Declarative update, rollback and manifest realignment |
| 10 | `05-rollout.yaml` | Four replicas, update budget observed, rollback |
| 11 | `daemonset.yaml` | One node-agent Pod per eligible node |
| 12 | drain / uncordon | Selected workloads leave the worker; scheduling re-enabled |
| 13 | `00_es03_*`, `01_es03_*`, `02_es03_*`, `03_es03_mongo-init.js` | NFS PVCs, DNS, one PRIMARY and two SECONDARY members |
| 14 | Kubernetes nginx-app example | Several objects from one YAML file |
| 15 | `ex06/app.yaml` | Adminer login and a MariaDB row surviving Pod replacement |
| 16 | `ex07/voting.yaml`, `ex07/vote-update.yaml` | Vote reaches result, three vote replicas after update |

`slide-map.json` gives the actual integrated slide numbers. `commands.txt` is a
guided transcript, **not an unattended script**: commands can block, require a
second terminal, initialize MongoDB once, or remove named exercise resources.
The exercise 7 manifests are intentionally left for the student to write.
Add a readiness probe on the client port (5000): without it, rollout completion
can precede Gunicorn accepting requests and the first HTTP call can fail.

## Application details

Exercise 6 uses `redis-backend` and `redis-client`. Exercise 15 uses `mariadb`,
`adminer` and PVC `mariadb-data`; Voting App retains `db`, `redis` and PVC `db-data`.
Names and selectors are distinct so the applications do not collide in `default`.

For exercise 15, open `http://127.0.0.1:18080` while forwarding the frontend.
Choose MySQL, server `mariadb`, database `books`, user `student`, password
`student-lab-only`. Insert a row, replace the database Pod, reconnect and confirm
that the row remains. The initialization ConfigMap runs only with an empty data
directory. MariaDB has one replica and Recreate strategy; persistence is not HA.

For exercise 16, forward vote on port 5000 and result on port 5001 in separate
configured terminals. One browser identity changes its existing vote. Use
separate browser profiles to represent distinct voters. The supplied update
changes a Pod-template annotation and replica count, not the application image.
Redis is ephemeral; PostgreSQL uses a PVC. Sample credentials are public teaching
values for the isolated lab, not production credentials.

Port 18080 avoids the provider API on 8080. Stop an earlier forwarding session
before reusing a local port. A Service port-forward chooses one backend Pod and
can need restarting when that Pod is replaced. For NodePort, use the
`LOCAL_ENDPOINT` from `lab services`; publication is asynchronous. On a remote
Docker host, also arrange an SSH tunnel from your laptop for the forwarded port.

MongoDB initialization and authentication are described in `MONGODB.txt`.
Inspect `spec.nfs.server` and `spec.nfs.path` in `kubectl get pv -o yaml` and use
`cloud volumes list`. `/var/hostpath-provisioner/default` is not this lab's storage.

## Cleanup

Stop port-forwards first. Remove constrained/Redis workloads before drain.
Drain selects only Pods labelled `course-lab=lecture02` and ignores DaemonSets; investigate blocking Pods
instead of adding force or emptyDir-deletion flags. Uncordon does not rebalance
existing Pods.

Before deleting a persistent application, record its PVC's `spec.volumeName`
(`mariadb-data` for Adminer/MariaDB, `db-data` for Voting App) and inspect
`cloud volumes list`. Remove only its manifest:

```sh
kubectl delete -f ex06/app.yaml
kubectl delete -f ex07/voting.yaml
```

Run each command only when finished with that application. Never delete namespace
`default` or use `kubectl delete ... --all` for cleanup. The `cloud-retain` class keeps
backing storage. Delete only a recorded exercise volume after confirming that its
data is disposable and its status permits deletion. Do not delete every listed
volume and do not use `docker compose down -v` as a shortcut.
If a Released Kubernetes PV remains after deleting its disposable backing volume
through the provider, remove that recorded PV explicitly with `kubectl delete pv
<recorded-volume-id>`. Do not remove a Bound PV or a volume still containing data
you need.

Deleting these application manifests does not remove MongoDB in `default`. Keep that database and
its claims until their data is no longer required. Cluster deletion is a final,
deliberate action for the dedicated course cluster, not part of each exercise.

## Validation and provenance

Run `python3 validate.py` with PyYAML installed. It checks the migrated YAML
objects, selectors, ports, configuration/PVC references, strategies and the
worker helper under both Bash and zsh. The original `.yml` MongoDB files remain
separate and retain their earlier verification record.

Live replay on 2026-11-09 passed all 16 exercises in `default` on Docker
Desktop with Kubernetes v1.35.6: exercises 1–4 on one lab-small node, then two
workers added immediately before exercise 5. Resize preserved the seven existing
hello-go/web Pod identities. Checks covered cross-worker Redis, NodePort,
rollout/rollback, the update budget, a three-node DaemonSet and targeted drain.
MongoDB NFS mounts, DNS, replica-set initialization and administrator authentication
passed. Adminer/MariaDB retained an inserted row after Pod replacement. Voting App
passed two-voter and changed-vote checks through the result Socket.IO endpoint,
then completed its update to three vote replicas.

The initial setup instruction was corrected to inspect the namespace without
writing the read-only kubeconfig. A test-harness assertion after MongoDB user
creation was corrected; only the three disposable MongoDB test volumes were
recreated with explicit approval before repeating that verification successfully.
The interactive editor/shell checks were performed through equivalent
noninteractive inspection and commands. Application interfaces were exercised
through HTTP/Socket.IO rather than manual browser interaction.
Detailed logs and per-exercise coverage remain local and are not included in
version control. Historical checks before the namespace migration are separate.

Adapted from Dr. Federica Filippini's **CC-Esercitazione Inf06 - Kubernetes** and
the existing Kubernetes_3 material. The PowerPoint deck is distributed separately.
