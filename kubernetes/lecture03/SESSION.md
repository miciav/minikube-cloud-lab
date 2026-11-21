# Verification log — November 11, 2026

**HPA, VPA, CRD, MySQL, Cluster Autoscaler and KEDA passed in separate runs with cleanup verified.** The later CA and KEDA exercises used one lab-small node and separate cleanup steps. The two-lab-medium configuration below records the earlier runs. Original logs are in the Git-ignored `evidence/` directory. All times are UTC. Italy was one hour ahead.

## HPA, CRD and MySQL test environment

- Cluster `lecture03` had two `lab-medium` nodes, one control plane and one worker.
- Each node had a quota of 4 CPUs (`CPUQuota=400000`, `CPUPeriod=100000`) and an 8 GiB memory limit.
- The ARM64 Docker host had 11 CPUs and 16,745,623,552 bytes of memory (about 15.6 GiB).
- Kubernetes server v1.35.6 and project kubectl v1.35.10.
- CoreDNS used `cluster.local`. The provider had already installed Metrics Server.
- The default StorageClass was `cloud-standard`, backed by NFS with reclaim policy Delete.
- MySQL Operator 9.1.0-2.2.2 with MySQL Server, Router and Shell 9.1.0.
- [Provider images and digests](evidence/28-runtime-versions.log) and [quotas and project client](evidence/29-client-and-quotas.log).

The initial run followed Kubernetes_4 slides 2–6, starting with one lab-small and growing to three nodes. After the problem recorded below, the instructor chose two lab-medium nodes for the rerun. The new cluster started with no volumes or workloads.

## Exercise 1 — HPA

**Objective.** Observe replicas increase under CPU load and decrease after load removal without setting the replica count manually.

We checked metrics, applied the Deployment, Service and HPA, waited for ScalingActive and recorded status. We ran the slide's wget loop interactively in busybox:1.28, watched the HPA, stopped the generator with Ctrl+C, waited for scale-down, deleted the HPA, Deployment and Service and checked that their Pods were gone.

| Observation | Result |
| --- | --- |
| Baseline | One replica, CPU 0%, target 50% |
| Scale-up | 1 → 3 → 4 → 6, with six replicas available |
| Load | HTTP responses contained OK! One sample reached 187% of the CPU request. |
| Stop | Ctrl+C at about 10:07:35. The --rm option removed the load generator. |
| Scale-down | Six → three at 10:12:38. One replica requested at 10:12:53. |
| Final observation | One available replica and CPU 0% at 10:13:08 |
| Cleanup | Only the Kubernetes Service remained among the queried resources in default |

Scale-down took about five and a half minutes after the load stopped, consistent with the default stabilization period used in this run. We left that period unchanged. Metrics were initially missing before the first collection. After ScalingActive, the API timeouts from the first attempt did not recur.

[Setup](evidence/27-hpa-medium-setup.log), [HPA events](evidence/30-hpa-medium-watch.jsonstream), [scale-up](evidence/32-hpa-medium-peak.log), [scale-down](evidence/33-hpa-medium-down.log), [cleanup](evidence/35-hpa-cleanup-verified.log). The generator exited with code 130 after Ctrl+C as expected. We closed the watch after the test.

## Exercise 3 — CRD and custom object

**Objective.** Extend the API with a resource type and observe which behavior requires a controller.

We applied the CRD, waited for Established=True and described it. We applied, retrieved and described my-new-cron-object, inspected Pods, Jobs and CronJobs, then deleted the object followed by the CRD.

The object retained cronSpec and image. No Pod, Job or CronJob was created. No CronTab objects or CRD remained after cleanup. Commands exited with code 0. On the new cluster, the test began after HPA cleanup finished.

[CRD test](evidence/36-crd-medium.log). The [first run](evidence/07-crd.log) also completed this exercise.

## Exercise 2 — MySQL Operator and InnoDB Cluster

**Objective.** Observe the operator turn a custom object into application resources, wait for convergence and connect through Router.

We installed the four upstream CRDs, waited for Established, installed the operator and waited for rollout. We created the exercise Secret, applied mycluster, watched until three members were ONLINE and Router was available, connected to root@mycluster in MySQL Shell, ran the queries, disconnected and exited with Ctrl+D.

At 10:19:34, about four minutes and ten seconds after creation:

- InnoDBCluster was ONLINE with all three members. All MySQL Pods were 2/2 with zero restarts.
- Router was 1/1. Three 2 GiB PVCs were Bound on cloud-standard.
- The connection through Service mycluster:3306 used SSL and reached mycluster-0 running version 9.1.0.
- Membership returned three ONLINE rows. mycluster-0 was PRIMARY. mycluster-1 and mycluster-2 were SECONDARY.
- Two instances ran on the worker and one on the control plane. This placement did not demonstrate tolerance of a node failure.

ONLINE also appeared with two members, so the guide explicitly checks onlineInstances=3 and Router availableReplicas=1. The final init container exited normally with code 0. PENDING, INITIALIZING and ONLINE_PARTIAL were intermediate states.

We kept the operator running during cleanup, deleted the custom object, waited for all three Pods to disappear and deleted the mypwds Secret. Because the PVCs survived InnoDBCluster deletion, we removed only the three recorded PVCs and checked that their PVs and cloud volumes disappeared. Finally we removed the operator, its namespace, RBAC and CRDs. MySQL Shell exited with code 0 and --rm removed the myshell Pod.

[Operator installation](evidence/37-mysql-medium-operator.log), [cluster readiness](evidence/38-mysql-medium-cluster.log), [full SQL session](evidence/42-mysql-shell.typescript), [SQL results](evidence/42-mysql-results.txt), [object cleanup](evidence/43-mysql-cleanup-objects.log), [storage and operator cleanup](evidence/44-mysql-cleanup-volumes-operator.log).

## First attempt — failure, consultation and recovery

The first cluster had three 2 GiB lab-small nodes. HPA reached five replicas. MySQL was started while HPA was waiting to scale down. The slides did not require the two exercises to overlap, and the final procedure runs them sequentially.

The API timed out, the Metrics API returned errors and HPA reported unknown metrics. The instructor initially authorized observation only. Interruptions continued and the scale-down wait expired. MySQL reached two online members. No SQL connection was attempted on that cluster. The diagnostic observations made with host kubectl v1.37.2 belong to this attempt.

Read-only diagnostics showed memory.current at 2,147,401,728 bytes against memory.max of
2,147,483,648 bytes, max counter 817148 and oom_kill=0. The RSS of mysqld was
808,040 KiB and the sidecar used 138,640 KiB RSS. The observations established memory pressure but could not identify it as the sole cause of the timeouts.

After the instructor authorized running the exercises separately, deleting the HPA through the API also failed with a TLS handshake timeout (exit 1). The instructor then authorized deleting the test cluster and its three volumes and creating two lab-medium nodes.

The provider stalled on orderly control-plane shutdown (`docker exec ... sudo init 0`). Running `docker stop --timeout 10 lecture03` on the node already marked for deletion allowed cleanup to finish. We removed the three Available/Retain volumes by ID and confirmed that 20 GiB was free before recreating the cluster.

**Successful recovery.** The exercises passed on two lab-medium nodes when run sequentially. Changing both conditions means the test cannot distinguish the effect of node size from the effect of sequential execution. We did not run MySQL alone on three lab-small nodes.

[Timeouts](evidence/19-diagnostics.log), [memory](evidence/21-node-pressure.log), [failed API deletion](evidence/23-hpa-cleanup.log), [cluster removal](evidence/24-delete-small-cluster.log), [targeted stop](evidence/25-stop-unresponsive-node.log), [volumes and replacement cluster](evidence/26-recreate-medium.log).

## State after HPA, CRD and MySQL, adaptations and limits

[Final check](evidence/45-final-state.log). Two nodes were Ready. No exercise Pod, Deployment, StatefulSet, ReplicaSet, HPA, PVC or Secret remained in default. There were no CRDs, PVs, mysql-operator namespace or cloud volumes. Storage showed 0 allocated and 20 GiB available. Of the queried base objects in default, only the kubernetes Service, kube-root-ca.crt ConfigMap and default ServiceAccount remained. Temporary command containers had exited. The cluster and provider services stayed up for further tests.

Changes from the slides included checking the provider's Metrics Server, correcting watch flag hyphens, keeping versioned upstream files locally, pinning the MySQL client to the operator tag and adding explicit waits, verification queries and cleanup. The original php-apache, hpa and mycluster manifests remained byte-identical.

Before CA was added, the scope was three visible exercises. Duplicate HPA slides counted as one exercise. PostgreSQL was on hidden slides. The original deck assigned no VPA, KEDA or Cluster Autoscaler experiments. The CA addition is recorded below. This first phase changed neither the PPTX files nor provider code. Failover, backups, MySQL upgrades and amd64 were not tested. Pre-existing changes to two lecture02 files were outside this work.

Static checks covered seven YAML manifests and seventeen objects. HPA, Service and Deployment selectors and targets matched, CR and CRD API versions and kinds matched, and SHA256SUMS recorded the checksums. These checks complemented the live runs.


## Added exercise — Cluster autoscaling

At the instructor's request six slides (9–14) were inserted after HPA, covering setup, installation, scale-up, scale-down, cleanup and review questions. The exercise reused `lab install/uninstall autoscaler`, `lab load start/stop` and existing manifests in `lab/manifests`. Provider code stayed unchanged. The other slides, including two hidden slides, retained their content and style.

The previous cluster was empty and had no volumes. The instructor authorized replacing it and leaving the new cluster at minimum size. `DELETE_CLUSTER op-000048` completed without manual intervention. We created `lecture03 --machine-type lab-small --min-nodes 1 --max-nodes 3` with `CREATE_CLUSTER op-000049`. We used Cluster Autoscaler v1.35.0, server v1.35.6 and the project wrappers.

| Time (UTC, November 11, 2026) | Observation |
| --- | --- |
| 10:37:29 | Creation began. One control plane, autoscaler available before load. |
| About 10:39:06 | Four Pods requested, three Pending, `Insufficient cpu` events |
| 10:39:17 | `op-000050` and `op-000051`, ADD_MACHINE from cluster-autoscaler |
| 10:39:45 | Four replicas Running, worker m02 Ready, m03 still joining |
| Before stopping | All three nodes Ready, both ADD_MACHINE operations DONE |
| 10:40:06 | `lab load stop` completed |
| 10:41:03 | DELETE_MACHINE m03, `op-000052`, DONE in about four seconds |
| 10:42:04 | DELETE_MACHINE m02, `op-000053`, DONE in about four seconds |
| 10:42:50 | Only the control plane Ready, no operations in progress, logs reported min size reached |
| 10:44:32 | Autoscaler uninstalled, no lab-load, PV, PVC, CRD or cloud volume |

We observed **1 → 3 → 1 nodes**. The tutorial allows a third node during scale-up. The slides require at least two Ready nodes. All replicas became available in about 40 seconds. The final cloud removal finished about two minutes after stopping the load. These times describe this run. We saw no API timeouts, system Pod restarts or operation errors.

We left `lecture03` RUNNING with one Ready lab-small node, MIN 1 / MAX 3. The provider, NFS, portal and dashboard remained active. The last node measurement was about 1019 MiB out of 2 GiB. The autoscaler and load were removed. At the instructor's request we left the lab-medium cluster unrestored. We did not run the optional ten-replica variant.

[Removal](evidence/51-ca-recreate.log), [setup](evidence/52-ca-setup.log), [scale-up](evidence/53-ca-scale-up.log), [autoscaler logs and stop](evidence/55-ca-scale-down-start.log), [scale-down](evidence/56-ca-scale-down-observe.log), [final cleanup](evidence/57-ca-cleanup.log). See the [exercise guide](CLUSTER_AUTOSCALING.md).


## Added exercise — KEDA Cron scaler

At the instructor's request we looked for a simple tutorial and chose the [official Cron scaler example](https://keda.sh/docs/2.21/scalers/cron/), following [Setup Autoscaling](https://keda.sh/docs/2.21/setupscaler/) and adapting the Deployment from the [HTTP tutorial](https://keda.sh/docs/2.21/deploy/). The time window demonstrates ScaledObject, HPA and scale-to-zero without a broker, external credentials or Prometheus. Queue triggers and application event consumption were outside this test.

We imported the official KEDA v2.21.0 release (26 objects) and its Apache-2.0 license. The manifest SHA-256 matched the GitHub release digest `b43c89ffeef81722d7e2dd2c079d74789767a0f89cae1336cff784994814f6d7`. Official compatibility includes Kubernetes 1.35. We applied the full manifest, including the webhook, unchanged using server-side apply. [Adaptations, sources and licenses](keda/README.md).

After CA cleanup we reused one Ready lab-small node with no previous workload, KEDA installation or external.metrics API. Three KEDA Deployments were available and the external API was Available at 10:49:30 UTC. The operator log attributed an initial voluntary exit to updated certificate Secrets (`RestartOnSecretRefresh`). No manual restart or correction was needed. We observed no OOM.

The `keda-http` Deployment used ARM64-compatible `hashicorp/http-echo:1.0.0`, requests of 25m/16Mi, limits of 100m/64Mi and an HTTP readiness probe. We waited for the first replica before the window to separate image pull time from scaling. ScaledObject used min 0 / max 2, polling 5s, cooldown 20s and `Etc/UTC`, with start 10:53 and end 10:55.

| Observation (UTC) | State |
| --- | --- |
| 10:51:31 | ScaledObject Ready. KEDA created the HPA and scaled the initial target from 1 to 0. |
| 10:51:47–10:52:51 | Zero replicas, Active=False |
| 10:53:01 (operator log) | KEDA activated the Deployment from 0 to 1 |
| 10:53:24 | Two replicas available, Active=True, HPA SuccessfulRescale event to 2 |
| 10:55:01 | Window closed, Active=False, two replicas still present |
| 10:55:16 (operator log) | KEDA scaled the Deployment from 2 to 0 |
| 10:55:17 and subsequent samples | Zero replicas confirmed |

Cooldown starts at the last active observation, sampled every five seconds. The target need not reach zero exactly 20 seconds after the end minute. HPA shows a minimum of one and KEDA controls zero. At zero, HPA metrics showed `<unknown>` while the ScaledObject was Ready with Active=False and the Deployment had zero replicas. That reading alone did not indicate failure. No manual replica changes occurred. Node count stayed at one.

Five KEDA slides were inserted immediately after CA as slides 15–19. The final deck contained 33 slides, two hidden. The original 22 retained their content. We opened and checked the eleven new slides in PowerPoint. The Redis/queue variant and the HTTP/Prometheus example were not run.

[Preflight](evidence/58-keda-preflight.log), [installation](evidence/59-keda-install.log), [certificate restart](evidence/60-keda-startup-diagnostics.log), [workload](evidence/61-keda-workload.log), [window](evidence/62-keda-window.txt), [scaling](evidence/63-keda-scaling.log), [details and events](evidence/64-keda-result-details.log).


### Final state after all exercises

KEDA cleanup finished at **10:58:09 UTC**. We removed the ScaledObject and HPA, then the exercise Deployment, then all 26 KEDA installation objects. Reading all manifest objects with `--ignore-not-found` returned nothing. No keda namespace, KEDA CRDs, external.metrics API, exercise workloads, HPA, PVC or cloud volume remained. The lecture03 lab-small node was Ready and system Pods had zero restarts. The provider retained MIN 1 / MAX 3. Cluster Autoscaler was uninstalled. The cluster, dashboard and cloud services stayed active as requested. [KEDA cleanup and final state](evidence/65-keda-cleanup.log).

Final material checks covered ten YAML files and 45 objects. Checksums passed, the release manifest matched the GitHub digest, local Markdown links resolved and the ScaledObject targeted the intended Deployment. We checked the final deck in PowerPoint. Original fonts, layouts and slide assets were preserved.


## KEDA rerun — November 11, 2026, 12:26–12:32 UTC

We installed from scratch using the distributed manifests and guide on the same cleaned lab-small node. We checked the lecture03 context and default namespace first. We set the window in `keda/scaledobject.yaml` to **12:29–12:31 UTC** and updated its SHA-256 in `SHA256SUMS`.

- Before the window there were zero replicas, Ready=True and Active=False.
- 12:29:10 — one replica available, Active=True.
- 12:29:22 — two available replicas. We verified that the ScaledObject owned the HPA.
- HTTP through temporary localhost port forwarding returned 200 and `Hello, KEDA!`. We closed the forward immediately after checking.
- 12:31:08 — Active=False with two replicas still present during cooldown.
- 12:31:19 — zero available and desired replicas. Automated assertions on JSON samples confirmed the full cycle without manual scaling.
- 12:32:02 — cleanup complete. No upstream manifest objects, CRDs, external.metrics API, keda namespace, exercise Pods/HPA/PVC or cloud volumes remained. One lecture03 lab-small node was Ready.

All three KEDA components restarted once initially. The operator exited voluntarily after refreshing certificates. The metrics API server and admission webhook initially lacked `/certs/ca.crt` and `/certs/tls.crt`, respectively. All rollouts and the external API then became available, with stable counters for the rest of the run. We updated the guide to check cause, convergence and stability for all components and explicitly check context and namespace. Neither the upstream manifest nor provider code needed changes.

We compared KEDA slides 15–19 with the manifests. Names, version, 0–2 limits and cooldown matched. The rerun required no deck changes. Cluster Autoscaler, CRD and MySQL were outside this rerun.

[Installation](evidence/keda-rerun-20261111-1226/02-install.log), [startup diagnostics](evidence/keda-rerun-20261111-1226/03-startup.log), [cycle assertions](evidence/keda-rerun-20261111-1226/04-cycle.log), [HTTP](evidence/keda-rerun-20261111-1226/05-http.txt), [cleanup](evidence/keda-rerun-20261111-1226/07-cleanup.log). The JSON samples and observation script are in the same local, Git-ignored `evidence/` directory.


## Cluster Autoscaling rerun with kubectl — November 11, 2026

We revised slides 9–14 and the guide to create all Kubernetes resources with `kubectl apply` from `kubernetes/lecture03`. The three files in `autoscaler/` are identical to `lab/manifests/autoscaler/`. Only `replicas: 0` differs between `load.yaml` and `lab/manifests/load.yaml`. Students scale `kubectl -n default scale deployment/lab-load` to four replicas and then zero. The Deployment controller maintains the count, and Cluster Autoscaler adjusts nodes. No HPA is installed. Reapplying the zero-replica manifest during load would stop the experiment.

| Observation (UTC) | Result |
| --- | --- |
| 12:38:17 | One Ready lab-small, MIN 1 / MAX 3, no previous load |
| 12:39:05 | Applied seven local autoscaler objects and the initial zero-replica Deployment |
| 12:39–12:40 | Scaled to four. Three Pods Pending for insufficient CPU, then four available replicas and three Ready nodes |
| 12:39:34 | ADD_MACHINE op-000054 and op-000055 from cluster-autoscaler, both DONE |
| 12:40:46 | Scale to zero complete. Deployment retained, no load Pods |
| 12:41:21 | DELETE_MACHINE op-000056 for m03, DONE in four seconds |
| 12:42:21 | DELETE_MACHINE op-000057 for m02, DONE in four seconds, back to one node |
| 12:46:30 | Cleanup complete. No workload, autoscaler or cloud volumes. One Ready lab-small. |

Preflight found two objects from the controller's first run, the `cluster-autoscaler-status` ConfigMap and `cluster-autoscaler` Lease. The upstream manifests do not include them and `delete -f autoscaler/` leaves them behind. The procedure now deletes the controller with `--cascade=foreground` and then explicitly removes both objects. We tested this sequence and confirmed both were gone. There were no operation errors or API timeouts. The provider remained active at MIN 1 / MAX 3 with one node. We did not rerun KEDA, HPA, CRD or MySQL during this revision.

We visually checked the six revised slides in PowerPoint and updated their notes. The deck retained 33 slides and the content of the other slides. `SHA256SUMS` includes the four local manifest copies.

Local, Git-ignored evidence includes [preflight](evidence/ca-kubectl-20261111/01-preflight.log), [creation and scale-up](evidence/ca-kubectl-20261111/02-scale-up.log), [scale-down](evidence/ca-kubectl-20261111/03-scale-down.log), [convergence](evidence/ca-kubectl-20261111/04-convergence.log) and [cleanup and final DONE checks](evidence/ca-kubectl-20261111/05-cleanup.log).


## Deck automation and MySQL 26.10 — November 11, 2026

We added `scripts/verify-lecture03.py` alongside `verify-lab.py`, reusing its Compose transport and PASS/FAIL format. The order was HPA, Cluster Autoscaler, KEDA, CRD and MySQL. The runner used local manifests and changed only a temporary copy of the KEDA Cron schedule. Recreation required `--recreate-empty-cluster` and an empty dedicated lab. CI included the runtime-independent tests. **43 Python tests passed**, including those for the earlier script.

Live checks ran on Docker with Kubernetes 1.35.6 on ARM64 and the observed DNS domain `cluster.local`:

- HPA started with one replica, grew under load and returned to one available replica without shortening stabilization. The generator and resources were removed.
- Cluster Autoscaler ran Deployment 0 → 4 → 0 and nodes 1 → 2 → 1. The test's ADD_MACHINE and DELETE_MACHINE operations reached DONE with requester cluster-autoscaler. The generated status ConfigMap and Lease were removed.
- KEDA used a 13:23–13:25 UTC window, scaled 0 → 2 → 0, confirmed HPA ownership and returned `Hello, KEDA!` over HTTP. Cleanup completed.
- CRD registered CronTab, verified its spec and removed all resources.

The first check exposed a runner error. It imposed the default namespace on operator bundles. We fixed both apply and delete so bundles keep the namespaces declared in their manifests while teaching resources explicitly use default. A regression test covers KEDA objects across multiple namespaces.

MySQL 9.1 passed the SQL query, but foreground deletion removed the internal `mycluster-privsecrets` Secret too early, causing operator 404s and stuck finalizers. The runner stopped and preserved the cluster. With explicit authorization, we deleted the lab and only the three recorded volumes. We did not force finalizers. InnoDBCluster deletion now uses background propagation, as in the manual procedure, so the operator retains access to its Secrets until finalization completes. A regression test covers this distinction.

At the instructor's request we replaced local manifests with the official **26.10.0-2.3.1** tag without patching upstream, and updated checksums, commands and MySQL Shell. We added an Established wait for the new `mysqlclustersetfailovers.mysql.oracle.com` CRD. Earlier 9.1 entries record the original test.

We reran only MySQL using the updated script methods on two fresh lab-medium nodes. At **13:45:38 UTC**, the cluster was ONLINE with three members. The query through Router returned version **26.10.0** and three ONLINE members, one PRIMARY and two SECONDARY. **Cleanup passed** for the custom object, Pods, PVCs, PVs, cloud volumes, exercise Secret, operator and CRDs. We did not repeat the four previously successful exercises after changing MySQL and final shutdown. The earlier full report still records the original cleanup failure.

During the rerun the instructor requested final cluster deletion. The test already in progress finished the old one-node restoration. We then executed and verified the new `delete_empty_cluster` method. The delivered runner deletes the cluster directly after MySQL cleanup. **Final state: no cluster, no volumes and no lecture03 node containers.** Cloud services stay active. On error or incomplete cleanup the runner stops for inspection and preserves the cluster.

Local, Git-ignored evidence includes the [five-exercise run and original cleanup failure](evidence/automation-20261111/deck/report.json), [cleanup diagnostics](evidence/automation-20261111/mysql-cleanup-failure.log), [MySQL 26.10 rerun](evidence/automation-20261111/mysql26/report.json), [SQL query](evidence/automation-20261111/mysql26/mysql-query.txt) and [final deletion](evidence/automation-20261111/final-deletion/report.json).

[Imported manifests and tag notes](mysql-operator-26.10.0-2.3.1/README.md). We did not run a live Podman test. The shared transport had runtime-independent tests. This suite version omitted VPA and the hidden PostgreSQL slides. The VPA run is documented below. The PPTX outside the repository was unchanged by this automation and MySQL update.


## VPA 1.7.1 — Off and Recreate

Separate November 11, 2026 run on Docker ARM64, Kubernetes 1.35.6, lecture03 with two lab-medium nodes. The runner now includes VPA immediately after HPA. The five previously verified exercises were not repeated in this VPA session.

| Checkpoint | Observation |
| --- | --- |
| Setup | Three controllers available, TLS webhook registered |
| Initial state | Two Pods, 50m CPU and 16Mi memory requests, limits 500m / 64Mi |
| Off | CPU target 500m (uncappedTarget 587m), original UIDs and 50m requests |
| Recreate | Two new Ready UIDs, both requesting 500m CPU, EvictedByVPA events |
| Invariants | Two replicas, template CPU 50m, unchanged memory and limits, same two nodes |
| Cleanup | Workload, CRDs, RBAC, controllers, webhook, TLS Secret and Lease removed |
| End | lecture03 deleted, no remaining cluster or volumes |

[Report and snapshots](evidence/vpa-1.7.1/) are excluded from Git. All five steps of the focused run passed. All 45 Python runner tests passed, including TLS certificate verification and comparison of replacement Pods. Podman and in-place resource updates were not verified. The 500m observation reflects maxAllowed=500m for this run. A few minutes of observation demonstrate the mechanism but cannot establish optimal service sizing.

Teaching materials include five VPA slides after the theory in a separate `Kubernetes_5_con_VPA.pptx` copy (38 slides), with notes and sources. We checked them in PowerPoint, preserved the original deck being edited and corrected references in the copy to lecture04 and the previous MySQL release.
