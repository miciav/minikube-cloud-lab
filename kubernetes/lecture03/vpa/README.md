# Vertical Pod Autoscaler — observe recommendations and apply them

Increase the **CPU request per Pod** while keeping two replicas. A CPU loop in BusyBox provides load without external services or traffic. First observe recommendations in Off mode, then watch Pod replacement in Recreate mode. Allow 10–15 minutes plus cluster creation.

## 1. Prepare the cluster

Use the dedicated lecture03 cluster with **two lab-medium nodes** from the [lecture setup](../README.md#setup-hpa--vpa--crd--mysql). Remove HPA, php-apache and load-generator from the previous exercise. Use a cluster without an existing VPA installation. The host needs Python 3 and OpenSSL.

From the repository root in each terminal:

```sh
source scripts/configure.sh
cloud clusters get-credentials lecture03
kubectl get nodes
kubectl top nodes
kubectl -n default get pods,deployments,hpa
kubectl get crd
cd kubernetes/lecture03/vpa
```

Check for two Ready nodes, available metrics, no previous workload and no installed `verticalpodautoscalers.autoscaling.k8s.io` CRD. This exercise uses VPA 1.7.1 and was tested on Kubernetes 1.35.6.

## 2. Install VPA

The six official manifests are in upstream/. Install CRDs and RBAC, generate the webhook TLS Secret, then apply the whole directory.

```sh
kubectl apply -f upstream/vpa-v1-crd-gen.yaml -f upstream/vpa-rbac.yaml
kubectl wait --for=condition=Established crd/verticalpodautoscalers.autoscaling.k8s.io crd/verticalpodautoscalercheckpoints.autoscaling.k8s.io --timeout=60s
python3 generate-tls.py | kubectl apply -f -
kubectl apply -f upstream/
kubectl -n kube-system rollout status deployment/vpa-recommender --timeout=5m
kubectl -n kube-system rollout status deployment/vpa-updater --timeout=5m
kubectl -n kube-system rollout status deployment/vpa-admission-controller --timeout=5m
kubectl get mutatingwebhookconfiguration vpa-webhook-config
```

Wait for the webhook before continuing. The helper uses OpenSSL and a private temporary directory. It emits only the Secret JSON manifest for kubectl apply. Certificates last seven days. Regenerate them for every installation and keep the output out of Git and logs. The admission controller registers the webhook with the matching CA.

## 3. Observe recommendations without changing Pods

Read deployment.yaml. Each of two replicas requests **50m CPU / 16Mi** with limits of **500m CPU / 64Mi**. 50m is 0.05 core. The CPU limit caps the loop's usage, while the scheduler uses the request.

```sh
kubectl apply -f deployment.yaml
kubectl -n default rollout status deployment/vpa-demo --timeout=5m
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
kubectl apply -f vpa.yaml
kubectl -n default top pods -l app=vpa-demo
kubectl -n default describe vpa vpa-demo
```

Record the initial names and UIDs. Repeat describe until the recommended CPU exceeds 100m, which may take several minutes. The RequestsOnly policy controls the CPU request with a 100m minimum and 500m maximum. The initial 50m request is deliberately below the minimum to make the subsequent change visible.

```sh
kubectl -n default get vpa vpa-demo -o 'jsonpath={.status.recommendation.containerRecommendations}'
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
```

**Off checkpoint.** A recommendation appears. UIDs and 50m requests stay the same. The recorded target was capped at 500m by policy. The exact value depends on collected metrics. A few minutes of observation demonstrate the mechanism but are insufficient for production sizing.

## 4. Apply recommendations with Recreate

```sh
kubectl -n default patch vpa vpa-demo --type=merge -p '{"spec":{"updatePolicy":{"updateMode":"Recreate"}}}'
kubectl -n default get pods -l app=vpa-demo -w
```

Wait for both Pods to be replaced and both new Pods to be Ready, then press Ctrl+C. Leave deletion and scaling to the controllers. The updater evicts the Pods, the Deployment recreates them and the admission controller adjusts requests at creation.

```sh
kubectl -n default get events --field-selector reason=EvictedByVPA
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,MEM:.spec.containers[0].resources.requests.memory,CPU_LIMIT:.spec.containers[0].resources.limits.cpu,MEM_LIMIT:.spec.containers[0].resources.limits.memory'
kubectl -n default get deployment vpa-demo -o 'jsonpath={.spec.replicas}{" replicas; template CPU: "}{.spec.template.spec.containers[0].resources.requests.cpu}{"\n"}'
kubectl get nodes
```

**Recreate checkpoint.** Two new UIDs, CPU requests above 50m and at most 500m, memory requests of 16Mi and unchanged 500m/64Mi limits. The Deployment template still specifies **two replicas and 50m CPU**. The new requests appear on Pods. The node count stays the same. Both requests reached 500m in the recorded run.

Why did the UIDs change? Which component recommends resources and which applies them? Compare HPA (replicas), VPA (resources per Pod) and Cluster Autoscaler (nodes). Leave CPU-based HPA off this workload during the exercise.

## 5. Cleanup

Remove the VPA object and workload before removing the installation and its generated objects.

```sh
kubectl delete -f vpa.yaml -f deployment.yaml --cascade=foreground --timeout=180s
kubectl delete -f upstream/ --cascade=foreground --timeout=180s
kubectl delete mutatingwebhookconfiguration/vpa-webhook-config --ignore-not-found
kubectl -n kube-system delete secret/vpa-tls-certs lease/vpa-updater lease/vpa-recommender lease/vpa-recommender-lease --ignore-not-found
kubectl -n default get pods,deployments,hpa
kubectl get crd
kubectl -n kube-system get deployment vpa-recommender vpa-updater vpa-admission-controller --ignore-not-found
```

Check for leftover exercise workloads, VPA controllers and VPA CRDs. Remove only the dedicated installation. If continuing to Cluster Autoscaler, use its guide to recreate the empty cluster at the required size. **At the end of the lab**, run:

```sh
cloud clusters delete lecture03
cloud clusters list
cloud volumes list
```

The automatic deck test checks these conditions and deletes the cluster at the end. Run `python3 scripts/verify-lecture03.py --recreate-empty-cluster` from the repository root. It saves diagnostics and stops on error. Consult the instructor before retrying or changing the cluster.

## Sources and provenance

- [Official VPA 1.7.1 quick start](https://github.com/kubernetes/autoscaler/blob/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/docs/quickstart.md), including the hamster example and observation of requests.
- [Official installation guide](https://github.com/kubernetes/autoscaler/blob/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/docs/installation.md).
- `upstream/*.yaml` contains unchanged copies of the six corresponding files from [vertical-pod-autoscaler/deploy/](https://github.com/kubernetes/autoscaler/tree/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/deploy) at tag vertical-pod-autoscaler-1.7.1. The Apache 2.0 license is included. SHA-256 checksums are in ../SHA256SUMS.
- Teaching adaptations include multiarch BusyBox with bounded CPU usage, CPU-only recommendations, Off → Recreate, two replicas and temporary local TLS. The exercise requires no Helm, cert-manager, external service or in-place update.
