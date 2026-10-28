#!/usr/bin/env bash
# Run against an isolated, ready cluster and a cloud budget of at least 256Mi.
set -euo pipefail
cloud() { docker compose exec -T cloud cloud "$@"; }
wait_available() {
  local volume=$1
  for _ in $(seq 1 120); do
    if cloud volumes list | grep -F "$volume" | grep -q Available; then return; fi
    sleep 2
  done
  return 1
}
wait_class() {
  for _ in $(seq 1 120); do
    if kubectl get storageclass cloud-static >/dev/null 2>&1; then return; fi
    sleep 2
  done
  return 1
}
claim() {
  local name=$1 class=$2 capacity=$3 volume=${4:-}
  kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $name
spec:
  storageClassName: $class
  ${volume:+volumeName: $volume}
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: $capacity
YAML
}
pod() {
  local name=$1
  kubectl apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $name
spec:
  securityContext:
    runAsUser: 1000
    runAsGroup: 1000
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
        claimName: $name
YAML
  kubectl wait --for=condition=Ready "pod/$name" --timeout=5m
}
wait_class
claim capacity-proof cloud-standard 64Mi
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/capacity-proof --timeout=5m
pod capacity-proof
kubectl exec capacity-proof -- sh -c '
  if dd if=/dev/zero of=/data/full bs=1048576 count=80 conv=fsync; then
    echo "ERROR: writes exceeded volume capacity" >&2; exit 1
  fi
  rm /data/full
  # Unlinking an open file must not bypass the filesystem limit.
  exec 3>/data/unlinked
  dd if=/dev/zero bs=1048576 count=40 conv=fsync >&3
  rm /data/unlinked
  if dd if=/dev/zero of=/data/other bs=1048576 count=40 conv=fsync; then
    echo "ERROR: unlinked open data escaped capacity accounting" >&2; exit 1
  fi
  exec 3>&-
  rm /data/other
  for attempt in 1 2 3 4 5; do
    if dd if=/dev/zero of=/data/after-close bs=1048576 count=40 conv=fsync; then break; fi
    rm /data/after-close
    sleep 1
  done
  test "$(wc -c < /data/after-close)" -eq 41943040
  rm /data/after-close
  printf capacity-survives > /data/message
  sync
'
# A request larger than the remaining budget must never allocate a PV.
total=$(cloud storage status | tail -n 1 | awk '{print $1}')
claim capacity-denied cloud-standard "$total"
for _ in $(seq 1 120); do
  if kubectl get events -o json | jq -e 'any(.items[]; .reason == "StorageCapacityExceeded" and .involvedObject.name == "capacity-denied")' >/dev/null; then break; fi
  sleep 2
done
kubectl get events -o json | jq -e 'any(.items[]; .reason == "StorageCapacityExceeded" and .involvedObject.name == "capacity-denied")'
[[ $(kubectl get pvc capacity-denied -o jsonpath='{.status.phase}') == Pending ]]
[[ -z $(kubectl get pvc capacity-denied -o jsonpath='{.spec.volumeName}') ]]
kubectl delete pvc capacity-denied
cloud storage status

# PVC-first static provisioning: no data resource is created until the admin acts.
volume=$(cloud volumes create --capacity 64Mi)
claim static-proof cloud-static 64Mi "$volume"
[[ $(kubectl get pvc static-proof -o jsonpath='{.status.phase}') == Pending ]]
cloud volumes publish "$volume" --cluster "${TEST_CLUSTER:?set TEST_CLUSTER}"
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/static-proof --timeout=5m
pod static-proof
kubectl exec static-proof -- sh -c 'printf static-survives > /data/message; sync'
kubectl delete pod static-proof --wait=true
kubectl delete pvc static-proof --wait=true
kubectl wait --for=jsonpath='{.status.phase}'=Released "pv/$volume" --timeout=3m
for _ in $(seq 1 120); do
  if cloud volumes unpublish "$volume"; then break; fi
  sleep 2
done
wait_available "$volume"
# PV-first static provisioning, reusing the retained data.
cloud volumes publish "$volume" --cluster "$TEST_CLUSTER"
claim static-proof cloud-static 64Mi "$volume"
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/static-proof --timeout=5m
pod static-proof
[[ $(kubectl exec static-proof -- cat /data/message) == static-survives ]]
kubectl delete pod static-proof --wait=true
kubectl delete pvc static-proof --wait=true
kubectl wait --for=jsonpath='{.status.phase}'=Released "pv/$volume" --timeout=3m
for _ in $(seq 1 120); do
  if cloud volumes unpublish "$volume"; then break; fi
  sleep 2
done
wait_available "$volume"
cloud volumes delete "$volume"
[[ $(kubectl exec capacity-proof -- cat /data/message) == capacity-survives ]]
kubectl delete pod capacity-proof --wait=true
kubectl delete pvc capacity-proof --wait=true
for _ in $(seq 1 120); do
  if [[ $(cloud volumes list | wc -l) -eq 1 ]]; then break; fi
  sleep 2
done
[[ $(cloud volumes list | wc -l) -eq 1 ]]
cloud storage status
