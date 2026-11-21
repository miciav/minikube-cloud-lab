# KEDA — run an HTTP service during a scheduled window

Watch **0 → 2 → 0 replicas** on a single node. Opening and closing a time window provides the scaling signal without a broker, external account or load generator. KEDA registers the `ScaledObject` type, reconciles its configuration and creates an HPA. KEDA activates the workload from zero and returns it to zero. The HPA scales replicas while the workload is active.

## Sources and imported files

- Official tutorial: [Setup Autoscaling with KEDA](https://keda.sh/docs/2.21/setupscaler/).
- Official example: [Cron scaler with a fixed replica count](https://keda.sh/docs/2.21/scalers/cron/#example-fixed-number-of-replicas-scale-up-to-10-replicas-from-6am-to-8pm-and-scale-down-to-0-replicas-otherwise).
- Deployment adapted from the tutorial's [HTTP service](https://keda.sh/docs/2.21/deploy/#step-1-deploy-a-sample-application).
- The complete [v2.21.0 release manifest](https://github.com/kedacore/keda/releases/download/v2.21.0/keda-2.21.0.yaml) is included unchanged in `upstream/`, with its Apache-2.0 license in `upstream/LICENSE`.
- KEDA documentation is CC-BY-4.0, © KEDA Authors. The two teaching manifests are attributed adaptations.

The exercise names the workload keda-http, pins the ARM64-verified HTTP echo image to 1.0.0 and adds requests, limits and a readiness probe. Observing replicas requires no Service. A UTC Cron trigger, two-replica maximum, five-second polling and 20-second cooldown keep the demonstration short. The full HTTP tutorial uses Prometheus and application metrics.

Upstream tested KEDA 2.21 with Kubernetes 1.34–1.36. The lab uses Kubernetes 1.35.6 and pinned release images. [Compatibility](https://keda.sh/docs/2.21/operate/cluster/#kubernetes-compatibility).

## 1. Prepare the cluster and install KEDA

Finish Cluster Autoscaler scale-down, uninstall that controller and remove its load. Reuse the clean lab-small node. From the repository root:

```sh
source scripts/configure.sh
kubectl config current-context
kubectl config view --minify -o 'jsonpath={..namespace}'
kubectl get nodes
kubectl get deployment,pods,hpa,pvc -n default
kubectl get ns keda --ignore-not-found
kubectl get crd
kubectl get apiservice v1beta1.external.metrics.k8s.io --ignore-not-found
cd kubernetes/lecture03/keda
```

Check that the context is lecture03 or your chosen dedicated cluster and that the namespace is default (an empty value also means default). The application manifests and observation commands use default. If your current namespace differs, add `-n default` to application commands.

If KEDA or another external.metrics adapter is already present, stop and consult the instructor. This procedure installs and removes a dedicated copy. Leave Kubernetes Metrics Server (`metrics.k8s.io`) in place.

```sh
kubectl apply --server-side -f upstream/keda-2.21.0.yaml
kubectl -n keda rollout status deployment/keda-operator --timeout=5m
kubectl -n keda rollout status deployment/keda-metrics-apiserver --timeout=5m
kubectl -n keda rollout status deployment/keda-admission --timeout=5m
kubectl wait --for=condition=Available apiservice/v1beta1.external.metrics.k8s.io --timeout=120s
```

Check for three available Deployments and an Available external API. During initial certificate generation, the operator may exit voluntarily after refreshing Secrets. In the rerun, the webhook and metrics-apiserver also restarted when they started before `/certs/tls.crt` and `/certs/ca.crt` were available. Check the cause of each restart.

```sh
# Use --previous only for components that restarted:
kubectl -n keda logs deployment/keda-operator --previous
kubectl -n keda logs deployment/keda-metrics-apiserver --previous
kubectl -n keda logs deployment/keda-admission --previous
kubectl -n keda get pods
```

Proceed after rollout and API availability, with restart counts stable. If errors persist, save the logs and consult the instructor. Keep webhooks and TLS checks enabled.

## 2. Start the application and choose the window

```sh
kubectl apply -f deployment.yaml
kubectl rollout status deployment/keda-http --timeout=3m
date -u '+%H:%M UTC'
```

Wait for the image download first. **For each run**, edit the two times in `scaledobject.yaml`. Set start about two minutes ahead and end about two minutes after start. Keep `timezone: Etc/UTC`. Cron fields are minute, hour, day of month, month and day of week. For example, at 10:50 UTC:

```yaml
start: "52 10 * * *"
end: "54 10 * * *"
```

The window repeats daily. Update the recorded times before using the manifest. Start and end must differ. The Cron scaler keeps a Deployment active during the window, whereas a CronJob starts scheduled Jobs.

## 3. Observe activation and the return to zero

```sh
kubectl apply -f scaledobject.yaml
kubectl wait --for=condition=Ready scaledobject/keda-http-window --timeout=60s
kubectl get deployment keda-http -w
```

In a second terminal, source `scripts/configure.sh` from the repository root.

```sh
kubectl get scaledobjects,hpa
kubectl get pods -l app=keda-http
kubectl describe scaledobject keda-http-window
```

Record these checkpoints without manually adjusting replicas.

1. Before start: Ready=True, Active=False and zero Deployment replicas.
2. During the window: Active=True, two available replicas and the KEDA-generated HPA `keda-hpa-keda-http-window`.
3. After closing and cooldown: Active=False, zero replicas and no keda-http Pods. Polling and reconciliation may add several seconds.

The 20-second cooldown runs from the last active observation, sampled every five seconds. It governs the return to zero. HPA stabilization governs scale-down between positive replica counts. HPA may show minimum 1 or ScalingDisabled at zero. Observe the Deployment and ScaledObject together. Do not create a second HPA for the same Deployment or reapply deployment.yaml during the test; that file declares one initial replica for verification.

If the workload does not converge, collect diagnostics.

```sh
kubectl describe scaledobject keda-http-window
kubectl describe hpa keda-hpa-keda-http-window
kubectl -n keda logs deployment/keda-operator --since=10m
kubectl get events --sort-by=.lastTimestamp
kubectl top nodes
```

Keep timestamps and logs and consult the instructor if the test fails.

## 4. Clean up before the next exercise

After observing zero replicas, stop the watches with Ctrl+C. Delete the ScaledObject while KEDA is still running.

```sh
kubectl delete -f scaledobject.yaml
kubectl wait --for=delete hpa/keda-hpa-keda-http-window --timeout=60s
kubectl delete -f deployment.yaml
kubectl get pods,hpa -n default
# Only remove the dedicated KEDA installation created for this run:
kubectl delete -f upstream/keda-2.21.0.yaml --timeout=180s
kubectl get crd
kubectl get apiservice v1beta1.external.metrics.k8s.io --ignore-not-found
kubectl get ns keda --ignore-not-found
kubectl get nodes
```

Check that the exercise ScaledObject, HPA, Deployment and Pods are absent. The KEDA namespace, CRDs, webhook and external API should be gone, and the node should still be Ready. The upstream manifest includes cluster-scoped objects. Do not delete a shared KEDA installation.

After cleanup, use the [two-lab-medium setup](../README.md) before MySQL. [Recorded results](../SESSION.md).


## Recorded run — November 11, 2026

The window was 10:53–10:55 UTC. The Deployment was at zero before opening, one replica at 10:53:07 and two available at 10:53:24. Active=False at 10:55:01 and zero replicas observed at 10:55:17. These are polling observations. The single node stayed Ready. The initial replica verified the image. No manual scaling occurred during the KEDA cycle.

[Window and manifest](../evidence/62-keda-window.txt), [scaling sequence](../evidence/63-keda-scaling.log), [conditions and events](../evidence/64-keda-result-details.log).

Full cleanup was verified at 10:58:09 UTC. [Final log](../evidence/65-keda-cleanup.log).


### Second run — November 11, 2026

With the **12:29–12:31 UTC** window, two replicas were available at 12:29:22 and zero at 12:31:19. The HTTP response was `Hello, KEDA!`. Cleanup finished at 12:32:02 with one Ready lab-small node. The manifests contain the times from this run. Update them for the next one.

[Cycle](../evidence/keda-rerun-20261111-1226/04-cycle.log), [HTTP](../evidence/keda-rerun-20261111-1226/05-http.txt), [cleanup](../evidence/keda-rerun-20261111-1226/07-cleanup.log).
