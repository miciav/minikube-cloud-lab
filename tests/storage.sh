#!/usr/bin/env bash
set -euo pipefail

cloud() { docker compose exec -T cloud cloud "$@"; }
wait_class() {
  for _ in $(seq 1 120); do
    if kubectl get storageclass cloud-retain >/dev/null 2>&1; then return; fi
    sleep 2
  done
  return 1
}
claim() {
  local class=$1 name=${2:-}
  kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: storage-proof
spec:
  storageClassName: $class
  ${name:+volumeName: $name}
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
YAML
  kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/storage-proof --timeout=5m
}
pod() {
  kubectl apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: storage-proof
spec:
  containers:
    - name: proof
      image: busybox:1.37.0
      command: ["sleep", "3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: storage-proof
YAML
  kubectl wait --for=condition=Ready pod/storage-proof --timeout=5m
}
case ${1:?phase required} in
  prepare)
    wait_class
    claim cloud-standard
    pod
    kubectl delete pod storage-proof --wait=true
    kubectl delete pvc storage-proof --wait=true
    for _ in $(seq 1 120); do
      if [[ $(cloud volumes list | wc -l) -eq 1 ]]; then break; fi
      sleep 2
    done
    [[ $(cloud volumes list | wc -l) -eq 1 ]]
    claim cloud-retain
    pod
    kubectl exec storage-proof -- sh -c 'printf storage-proof > /data/message'
    kubectl get pvc storage-proof -o jsonpath='{.spec.volumeName}' > "$RUNNER_TEMP/storage-volume"
    kubectl delete pod storage-proof --wait=true
    ;;
  recover)
    volume=$(cat "$RUNNER_TEMP/storage-volume")
    cloud volumes list | grep -F "$volume" | grep -F Available
    cloud clusters create storage-recovery
    cloud clusters get-credentials storage-recovery
    wait_class
    claim cloud-retain "$volume"
    pod
    [[ $(kubectl exec storage-proof -- cat /data/message) == storage-proof ]]
    kubectl delete pod storage-proof --wait=true
    kubectl delete pvc storage-proof --wait=true
    for _ in $(seq 1 120); do
      if cloud volumes list | grep -F "$volume" | grep -q Available; then break; fi
      sleep 2
    done
    kubectl delete pv "$volume" --ignore-not-found
    cloud volumes delete "$volume"
    cloud clusters delete storage-recovery
    ;;
  *) exit 2 ;;
esac
