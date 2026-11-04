# Student tutorial: persistent storage and the cloud

For the Podman rootless VM variant, use [these setup instructions](podman-rootless.md). Its storage allocation budget is simulated and does not impose physical write quotas.

Use the runtime already started in the [autoscaling tutorial](student-tutorial.md):
`source scripts/configure-rootless.sh` for the Podman VM, or `source
scripts/configure.sh` for Docker. The helpers provide containerized `kubectl`;
no host installation is required. Pull images published by the CI before starting
an updated lab. The optional portal is available only in the Docker configuration.
Allocated capacity is a reservation, not measured disk usage.

```sh
cloud clusters create demo
cloud clusters get-credentials demo
kubectl get storageclass
```

The cloud installs `cloud-standard` (reclaim policy `Delete`) and `cloud-retain` (`Retain`). If they are not visible yet, wait a few seconds. Docker volumes use NFS storage provided by the cloud, with a bounded filesystem per volume. Podman rootless uses directory-backed volumes with a simulated allocation budget and no physical write quota. On Docker, capacity includes filesystem metadata and journal space, so usable file bytes are smaller than the nominal virtual disk size. The cloud also installs `cloud-static`, for administrator-created PVs.

## Request and inspect a volume

Create a claim and a Pod that uses it:

```sh
cat > storage-lab.yaml <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: lab-data
spec:
  storageClassName: cloud-retain
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: storage-lab
spec:
  containers:
    - name: shell
      image: busybox:1.37.0
      command: ["sleep", "3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: lab-data
YAML
kubectl apply -f storage-lab.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/lab-data --timeout=5m
kubectl wait --for=condition=Ready pod/storage-lab --timeout=5m
kubectl exec storage-lab -- sh -c 'echo hello-cloud > /data/message'
kubectl get pvc lab-data
kubectl get pv
cloud volumes list
```

The PVC is a Kubernetes request; the cloud volume is the storage resource provisioned to satisfy it. Note the `pvc-...` identifier shown by `cloud volumes list`. Kubernetes may report `Bound` before the Pod starts.

## Retain and recover

Delete the cluster, then inspect the retained volume:

```sh
cloud clusters delete demo
cloud volumes list
```

The volume becomes `Available`. Create another cluster, configure its credentials, and create a claim pointing to the retained volume. Replace `VOLUME_ID` with the identifier recorded above:

```sh
cloud clusters create restored
cloud clusters get-credentials restored
# Wait until the cloud-retain StorageClass is present.
kubectl get storageclass
cat > recover-lab.yaml <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: recovered
spec:
  storageClassName: cloud-retain
  volumeName: VOLUME_ID
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
YAML
kubectl apply -f recover-lab.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/recovered --timeout=5m
```

Create a Pod using the recovered claim, then read `/data/message`:

```sh
cat > recovered-pod.yaml <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: storage-lab
spec:
  containers:
    - name: shell
      image: busybox:1.37.0
      command: ["sleep", "3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: recovered
YAML
kubectl apply -f recovered-pod.yaml
kubectl wait --for=condition=Ready pod/storage-lab --timeout=5m
kubectl exec storage-lab -- cat /data/message
```

The file remains because the storage service is independent of the cluster.

## Clean up

Delete the Pod and PVC, and wait until `cloud volumes list` shows the retained volume as `Available`. Remove its released Kubernetes PV, then explicitly remove the cloud volume:

```sh
kubectl delete pod storage-lab --ignore-not-found
kubectl delete pvc recovered --ignore-not-found
cloud volumes list
kubectl delete pv VOLUME_ID --ignore-not-found
cloud volumes delete VOLUME_ID
cloud clusters delete restored
# Podman rootless VM:
labcompose down
# For Docker, use docker compose down instead.
```

With `cloud-standard`, deleting the PVC deletes the cloud volume after its Pod has stopped. Deleting a cluster instead preserves all remaining volumes as `Retain`: delete any unwanted available volumes explicitly with `cloud volumes delete`. Compose `down` preserves named storage volumes in both configurations; `down -v` destroys their contents. Only the Docker/NFS backend enforces physical per-volume size. Run the lab on a trusted computer.


## Explore the allocation budget and write limit

```sh
cloud storage status
```

The default total is `20Gi`. Set `CLOUD_STORAGE_CAPACITY=4Gi` in `.env` before
starting the cloud to use a smaller budget, then recreate the cloud service with
`docker compose up -d --force-recreate cloud` or, for the rootless VM,
`labcompose up -d --force-recreate cloud`. The sum of allocated virtual disks
cannot exceed that budget. Retained volumes still count, even after deleting
their PVCs or clusters. Reducing the budget never shrinks or deletes existing
volumes: it blocks new allocations until capacity is available again.

A PVC that would exceed the budget remains `Pending`:

```sh
kubectl describe pvc CLAIM_NAME
cloud storage status
cloud volumes list
```

Look for `StorageCapacityExceeded` in its events. Delete unwanted available cloud
volumes to release capacity; the provisioner retries the waiting PVC automatically.
A `ResourceQuota` is an additional Kubernetes namespace policy and does not
replace this cloud-wide allocation budget.

### Docker/NFS only: physical write limit

Skip this subsection on Podman rootless and use the oversized-PVC exercise
below instead. Rootless directories do not impose a physical write quota.
For Docker/NFS, create a disposable small PVC and Pod:

```sh
cat > quota-lab.yaml <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: quota-data
spec:
  storageClassName: cloud-standard
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 64Mi
---
apiVersion: v1
kind: Pod
metadata:
  name: quota-lab
spec:
  containers:
    - name: writer
      image: busybox:1.37.0
      command: [sleep, '3600']
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: quota-data
YAML
kubectl apply -f quota-lab.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/quota-data --timeout=5m
kubectl wait --for=condition=Ready pod/quota-lab --timeout=5m
kubectl exec quota-lab -- dd if=/dev/zero of=/data/full bs=1048576 count=80 conv=fsync
kubectl exec quota-lab -- rm /data/full
kubectl delete pod quota-lab --wait=true
kubectl delete pvc quota-data
```

The `dd` command must fail with a disk-full error. Use a disposable volume:
filling an application's filesystem can make it fail. Removing the test file
restores free space. The cloud budget accounts nominal
allocated disk capacity, not the number of bytes currently written. It does not
reserve host disk space; other Docker workloads can still exhaust the backing disk.

## Administrator-created storage (static provisioning)

This scenario uses the same cloud and a running cluster named `demo`. For a
separate session, repeat the setup at the beginning of this tutorial. The
administrator allocates storage first and publishes it as a PV:

```sh
volume=$(cloud volumes create --capacity 2Gi)
cloud volumes publish "$volume" --cluster demo
kubectl get pv "$volume"
cloud storage status
```

The PV is initially available, has class `cloud-static`, and uses reclaim policy
`Retain`. The student requests that volume:

```sh
cat > static-storage.yaml <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: static-data
spec:
  storageClassName: cloud-static
  volumeName: $volume
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 2Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: static-storage
spec:
  containers:
    - name: shell
      image: busybox:1.37.0
      command: [sleep, '3600']
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: static-data
YAML
kubectl apply -f static-storage.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/static-data --timeout=5m
kubectl wait --for=condition=Ready pod/static-storage --timeout=5m
kubectl exec static-storage -- sh -c 'echo admin-provided-storage > /data/message'
cloud volumes list
```

Kubernetes performs the binding; the cloud observes the resulting claim. You can
also create the PVC before the administrator publishes the PV: it remains
`Pending` until compatible storage is available. `cloud-static` never allocates
storage automatically. Without `volumeName`, Kubernetes can choose any compatible
available static PV.

Clean up the Kubernetes objects, then remove the publication:

```sh
kubectl delete pod static-storage --wait=true
kubectl delete pvc static-data --wait=true
kubectl wait --for=jsonpath='{.status.phase}'=Released "pv/$volume" --timeout=3m
cloud volumes unpublish "$volume"
cloud volumes list
```

The cloud volume is now `Available`, its data is preserved, and it still consumes
budget. Publish it again and recreate the PVC/Pod to read the original file.
Finally, permanently remove it:

```sh
cloud volumes delete "$volume"
cloud storage status
```

The cloud refuses to delete a published or bound volume. If Kubernetes protection
finalizers take longer to release a PV, retry `unpublish` after they finish.

## Request more capacity than the cloud can supply

Both paths enforce the allocation budget (`CLOUD_STORAGE_CAPACITY`, default
`20Gi`). This check does not allocate or fill a 100Gi disk:

```sh
cat > oversized-pvc.yaml <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: oversized-data
spec:
  storageClassName: cloud-standard
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 100Gi
YAML
kubectl apply -f oversized-pvc.yaml
kubectl get pvc oversized-data
kubectl describe pvc oversized-data
kubectl get events --field-selector reason=StorageCapacityExceeded
cloud storage status
```

**Checkpoint:** the claim remains Pending, no PV is created for it, and its
events contain `StorageCapacityExceeded`. Remove it with `kubectl delete pvc
oversized-data`. On the rootless VM, do not confuse this allocation refusal
with a write quota: writing beyond an accepted PVC's declared size is not blocked
by a per-volume filesystem limit. Physical disk exhaustion is still possible.
