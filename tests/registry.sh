#!/usr/bin/env bash
# Build an image in a debian-12 VM, push it to a managed registry and deploy it from there on the
# current cluster with a read-only pull Secret. Needs a running rootful cloud started with
# `docker compose` from this checkout and host kubectl pointing at a cluster created by it.
set -euo pipefail
cd "$(dirname -- "${BASH_SOURCE[0]}")/.."
BUSYBOX_IMAGE=${BUSYBOX_IMAGE:-docker.io/library/busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec}
cloud() { docker compose exec -T cloud cloud "$@"; }
registry=corso endpoint=corso.registry:5000
vm="reg-$(date +%s)-$$"
cleanup() {
    kubectl delete deployment hello --ignore-not-found --wait=false >/dev/null 2>&1 || true
    kubectl delete secret corso-pull --ignore-not-found >/dev/null 2>&1 || true
    cloud instances delete "$vm" </dev/null >/dev/null 2>&1 || true
    if [[ ${registry_created:-} == yes ]]; then cloud registries delete "$registry" --force </dev/null >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
must_reject() {
    local expected=$1 output
    shift
    if output=$(cloud "$@" </dev/null 2>&1); then echo "Unexpected success: $*" >&2; return 1; fi
    [[ "$output" == *"$expected"* ]] || { echo "$output" >&2; return 1; }
}
# The arguments are joined with spaces and parsed again by the guest shell, like OpenSSH: quote
# any argument holding spaces or shell characters inside one command string.
guest() { local vm=$1; shift; cloud instances ssh "$vm" -- "$@"; }
secret() { cloud registries credentials "$registry" "--$1" </dev/null | awk '$1 == "password" {print $2}'; }
wait_ready() { # restart the cloud and wait for its API
    for _ in $(seq 1 60); do if cloud machine-types list </dev/null >/dev/null 2>&1; then return; fi; sleep 2; done
    echo "The cloud did not come back" >&2
    return 1
}
lists_hello() { cloud registries images "$registry" </dev/null | awk '$1 == "hello" && $2 == "v1" {found = 1} END {exit !found}'; }

subnet=$(docker network inspect minikube-cloud-lab --format '{{(index .IPAM.Config 0).Subnet}}')
registry_created=yes # before any check, so the trap deletes it
cloud registries create "$registry" </dev/null | grep -Fx "Created registry $registry at $endpoint"
cloud registries list </dev/null | awk -v n="$registry" -v e="$endpoint" '$1 == n && $2 == e {f = 1} END {exit !f}'
cloud registries credentials "$registry" --pull </dev/null | grep -Fx 'username pull'
push_pw=$(secret push) pull_pw=$(secret pull)

cloud instances create "$vm" --image debian-12 </dev/null
# VMs are not cluster nodes: the student trusts the lab subnet for plain HTTP themselves.
# Keep whatever daemon.json the image ships.
config=$(guest "$vm" sudo cat /etc/docker/daemon.json </dev/null 2>/dev/null || true)
config=$(jq -c --arg cidr "$subnet" '."insecure-registries" = ((."insecure-registries" // []) + [$cidr] | unique)' <<<"${config:-"{}"}")
guest "$vm" sudo sh -eu <<SCRIPT
echo '$config' > /etc/docker/daemon.json
systemctl restart docker
for _ in \$(seq 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
mkdir -p /tmp/hello
printf 'FROM %s\nCMD ["httpd", "-f", "-p", "8080"]\n' '$BUSYBOX_IMAGE' > /tmp/hello/Dockerfile
SCRIPT
guest "$vm" "docker info --format '{{json .RegistryConfig.InsecureRegistryCIDRs}}'" </dev/null | grep -F "\"$subnet\""
guest "$vm" docker login -u push --password-stdin "$endpoint" <<<"$push_pw"
guest "$vm" docker build -q -t "$endpoint/hello:v1" /tmp/hello </dev/null
guest "$vm" docker push "$endpoint/hello:v1" </dev/null

# The pull account logs in but must not write.
guest "$vm" docker login -u pull --password-stdin "$endpoint" <<<"$pull_pw"
guest "$vm" docker tag "$endpoint/hello:v1" "$endpoint/hello:readonly" </dev/null
if out=$(guest "$vm" docker push "$endpoint/hello:readonly" </dev/null 2>&1); then echo "The pull account pushed" >&2; exit 1; fi
grep -v '^The push refers to repository' <<<"$out" | grep -qiE 'denied|unauthorized|forbidden' || { echo "$out" >&2; exit 1; }
lists_hello
if cloud registries images "$registry" </dev/null | awk '$2 == "readonly" {f = 1} END {exit !f}'; then echo "readonly tag stored" >&2; exit 1; fi

kubectl create secret docker-registry corso-pull --docker-server="$endpoint" --docker-username=pull --docker-password="$pull_pw"
kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
spec:
  replicas: 1
  selector:
    matchLabels: {app: hello}
  template:
    metadata:
      labels: {app: hello}
    spec:
      imagePullSecrets: [{name: corso-pull}]
      containers:
        - name: hello
          image: $endpoint/hello:v1
          imagePullPolicy: Always
          ports: [{containerPort: 8080}]
YAML
kubectl rollout status deployment/hello --timeout=3m
kubectl get pods -l app=hello -o json | jq -e '[.items[] | select(.status.phase == "Running")] | length == 1'

# A registry lives with the cloud: stopping the cloud removes its containers, so `down` can drop the
# network, and recreating the cloud brings it back on the same data.
docker compose stop cloud
leftover=$(docker ps -aq --filter label=minikube-cloud-lab.registry)
if [[ -n $leftover ]]; then echo "registry containers outlived the cloud: $leftover" >&2; exit 1; fi
docker compose up -d --force-recreate cloud
wait_ready
lists_hello
kubectl rollout restart deployment/hello # imagePullPolicy Always: the new Pod pulls again
kubectl rollout status deployment/hello --timeout=3m

kubectl delete deployment hello
kubectl delete secret corso-pull
cloud instances delete "$vm" </dev/null
must_reject 'delete with force' registries delete "$registry"
cloud registries delete "$registry" --force </dev/null | grep -Fx "Deleted registry $registry"
registry_created=
# The cloud must be able to remove the registry server's root-owned files.
docker compose exec -T cloud test ! -e "/data/registries/$registry"
if cloud registries list </dev/null | awk -v n="$registry" '$1 == n {f = 1} END {exit !f}'; then echo "$registry still listed" >&2; exit 1; fi
echo "PASS: VM push, read-only pull account, cluster deploy across a cloud restart, forced delete"
