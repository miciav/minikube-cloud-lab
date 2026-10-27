# Student tutorial: persistent storage and the cloud

This lab uses the same Docker Compose cloud as the [autoscaling tutorial](student-tutorial.md). Start it with `source scripts/configure.sh` and `docker compose up -d cloud`, then create a cluster and configure credentials:

```sh
cloud clusters create demo
cloud clusters get-credentials demo
kubectl get storageclass
```

The cloud installs `cloud-standard` (reclaim policy `Delete`) and `cloud-retain` (`Retain`). If they are not visible yet, wait a few seconds. Volumes use NFS storage provided by the cloud. The requested size is recorded but is not a filesystem quota.

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
docker compose down
```

With `cloud-standard`, deleting the PVC deletes the cloud volume after its Pod has stopped. Deleting a cluster instead preserves all remaining volumes as `Retain`: delete any unwanted available volumes explicitly with `cloud volumes delete`. `docker compose down` preserves the shared Docker storage volume; `docker compose down -v` removes it and destroys its contents. This lab does not enforce quotas or provide high availability. Run it on a trusted computer: the NFS export is accessible from the lab network.
