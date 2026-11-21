# Cluster autoscaling with kubectl

This exercise accompanies “Cluster autoscaling” in `Kubernetes_5.pptx`. Clean up each exercise before starting the next. Configure access at the **repository root**, then run manifest commands from **kubernetes/lecture03**. Use `kubectl apply` to create resources, `kubectl scale` to change the workload and `cloud` to manage the cluster and observe provider requests.

The [autoscaler manifests](autoscaler/) copy the existing project files. The only change to the original workload in [load.yaml](load.yaml) is `replicas: 0` in place of `replicas: 4`. [Project tutorial](../../docs/tutorials/autoscaling/kubernetes-nodes.md).

**Objective.** Connect unschedulable Pods to a Cluster Autoscaler decision, a cloud operation and the arrival of a Ready Node. Then observe scale-down. Students set Deployment replicas to **0 → 4 → 0**. The Deployment controller maintains that count and Cluster Autoscaler adjusts nodes. The experiment creates pressure through resource requests even at low measured CPU usage. It uses no HPA.

## 1. Prepare the cluster

Complete the previous exercise's cleanup, including the load generator. Check resources before replacing a cluster.

```sh
source scripts/configure.sh
docker compose up -d
cloud clusters list
kubectl config current-context
kubectl get deployments,statefulsets,pods,hpa,pvc -A
cloud volumes list
```

If no cluster exists, kubectl can still point at an old context. Use cloud inventory to choose the setup. The provider manages one cluster. Delete `lecture03` **only if dedicated, empty and disposable**. Consult the instructor about clusters with existing data or other users' resources.

```sh
# Only to replace the previous empty lecture03 cluster:
cloud clusters delete lecture03

# If no cluster already has this configuration:
cloud clusters create lecture03 --machine-type lab-small --min-nodes 1 --max-nodes 3
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes
cloud clusters describe lecture03
```

Check for one Ready lab-small control plane, MIN 1 / MAX 3. Each node is limited to 2 CPUs and 2 GiB. Allow for three nodes plus provider services. The `cloud clusters resize` command fixes min and max at the same count and retains the machine type.

## 2. Create and observe the resources

After setup, enter the exercise directory from the repository root.

```sh
cd kubernetes/lecture03
kubectl apply -f autoscaler/
kubectl -n kube-system rollout status deployment/cluster-autoscaler --timeout=5m
kubectl apply -f load.yaml
kubectl -n default get deployment lab-load
kubectl -n default get pods -l app=lab-load
```

Check that the autoscaler is available and `lab-load` exists with **zero replicas** and no Pods. Read the [Deployment](autoscaler/deployment.yaml), [RBAC](autoscaler/rbac.yaml) and [cloud-config](autoscaler/cloud-config.yaml) manifests. The controller uses `--cloud-provider=externalgrpc` and the internal endpoint `host.minikube.internal:9090`. Applying `autoscaler/` creates the controller's ServiceAccount, RBAC, ConfigMap and Deployment.

In two additional terminals, run `source scripts/configure.sh` from the repository root and start the watches **before generating load**.

```sh
# Terminal B
kubectl -n default get pods -l app=lab-load -w
# Terminal C
kubectl get nodes -w
```

Ctrl+C stops the watch. An empty Pod list is expected at first.

## 3. Scale up

Read [load.yaml](load.yaml). Each Pod requests 600m CPU and 64Mi. Four replicas request **2.4 CPUs**. Predict whether these Pods and the system Pods can fit on one 2-CPU node. The pause containers generate little CPU load.

In terminal A, from `kubernetes/lecture03`:

```sh
kubectl -n default scale deployment/lab-load --replicas=4
kubectl -n default get events --field-selector reason=FailedScheduling --sort-by=.lastTimestamp
kubectl -n default rollout status deployment/lab-load --timeout=5m
kubectl -n default get deployment lab-load
kubectl -n default get pods -l app=lab-load -o wide
kubectl get nodes
cloud machines list --cluster lecture03
cloud operations list
kubectl -n kube-system logs deployment/cluster-autoscaler --since=5m
```

Check for four available replicas, at least two Ready nodes and an `ADD_MACHINE` operation requested by `cluster-autoscaler` in state `DONE`. Record the Pending / `Insufficient cpu` event, operation ID, new node, `CREATED_BY` and Pod placement. Substitute the real ID in `cloud operations describe OPERATION_ID`.

The provider may receive a request for a third node while capacity settles. If rollout times out during provisioning, keep observing for up to 15 minutes total. On ERROR or failure to converge, save logs and events and consult the instructor. Leave node scaling to the autoscaler. Students set Pod replicas explicitly. Reapplying `load.yaml` during the four-replica phase would reset the Deployment to zero.

## 4. Scale down and clean up

Scale replicas to zero and keep the Deployment. Leave Cluster Autoscaler running until worker removal finishes.

```sh
kubectl -n default scale deployment/lab-load --replicas=0
kubectl get nodes -w
# In a second terminal, repeat until the checkpoint is reached:
cloud machines list --cluster lecture03
cloud operations list
```

Check that only the control plane is Ready, every worker DELETE_MACHINE operation requested by cluster-autoscaler is DONE and no operation is RUNNING. The Node can disappear before cloud teardown finishes. The lab uses one-minute delays and removes one worker at a time, so allow several minutes. Minimum size is one and the control plane is protected.

**After reaching that checkpoint**, stop the watches and uninstall the controller.

```sh
kubectl delete -f load.yaml
kubectl delete -f autoscaler/ --cascade=foreground
# Objects generated by the controller during this run:
kubectl -n kube-system delete --ignore-not-found \
  configmap/cluster-autoscaler-status lease/cluster-autoscaler
kubectl -n default get deployment,pods -l app=lab-load
kubectl -n kube-system get deployment cluster-autoscaler --ignore-not-found
kubectl get nodes
```

Expect one Ready node, no lab-load resources and no autoscaler Deployment. Leave the cluster running and clean. Wait for minimum size before uninstalling the autoscaler. Foreground deletion waits for the controller Pod to disappear. Then remove the generated status ConfigMap and Lease. Both survive deleting the installation manifest alone.

## 5. Continue to the next exercise

The [KEDA exercise](keda/README.md) follows CA in the deck. Reuse the cleaned lab-small node with Cluster Autoscaler uninstalled. Complete KEDA cleanup before proceeding.

For MySQL, restore the verified **two-lab-medium setup** described in the [lecture guide](README.md). Run the following only after full cleanup of the dedicated empty cluster.

```sh
cloud clusters delete lecture03
cloud clusters create lecture03 --machine-type lab-medium --min-nodes 2 --max-nodes 2
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
```

The restore commands were not run at the end of the manual CA test. The instructor requested leaving the cleaned cluster at its one-node minimum.

## Evidence and discussion

We repeated this procedure on November 11, 2026 at 12:39–12:46 UTC **using local manifests, kubectl apply and kubectl scale**. Deployment replicas went 0 → 4 → 0 and Ready nodes went 1 → 3 → 1. Both additions and both removals reached DONE. Cleanup also removed the controller's generated status ConfigMap and Lease. We saw no API timeouts or operation errors. [Rerun details](SESSION.md#cluster-autoscaling-rerun-with-kubectl--november-11-2026).

The first November 11 run used the project helpers. It started with one node, observed three Pods Pending for insufficient CPU, two automatic worker additions and four available replicas. After the load stopped, both workers were removed automatically and one node remained. We saw no API timeouts or operation errors. [First run and logs](SESSION.md#added-exercise--cluster-autoscaling).

- Who chooses four replicas? The student, with kubectl scale.
- Who maintains them? The Deployment controller.
- Who assigns Pods to nodes? The scheduler.
- Who requests capacity from the provider? Cluster Autoscaler.
- Who creates and removes machines? The minikube-cloud-lab provider.
- Why can Pods be Pending at low measured CPU usage? The scheduler accounts for requests, including capacity already committed to other Pods.

The optional ten-replica variant in the project tutorial was outside this exercise and was not tested. [Kubernetes node autoscaling](https://kubernetes.io/docs/concepts/cluster-administration/node-autoscaling/).
