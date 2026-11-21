#!/usr/bin/env bash
# Live checks shared by Docker Desktop and the Linux student workflow.
set -euo pipefail
: "${KUBECONFIG:?use an isolated lab kubeconfig}"
: "${TEST_CLUSTER:?managed test cluster}"
: "${TEST_NETWORK:?dedicated Docker network}"
: "${TEST_CLOUD_CONTAINER:?cloud container ID}"
: "${TEST_ARTIFACT_DIR:?directory for endpoint snapshots}"
mkdir -p "$TEST_ARTIFACT_DIR"
namespace_file="$TEST_ARTIFACT_DIR/exposure-namespace"
if [[ $1 != prepare && $1 != deleted ]]; then namespace=$(cat "$namespace_file"); fi
services=(mcl-test-node mcl-test-lb-one mcl-test-lb-two)
relay_ids() {
    docker ps -aq --filter label=minikube-cloud-lab.service-proxy=true \
        --filter "label=minikube-cloud-lab.cluster=$TEST_CLUSTER" \
        --filter "label=minikube-cloud-lab.network=$TEST_NETWORK"
}
# wait_for DESCRIPTION PREDICATE: retry for two minutes, then fail naming what never happened.
wait_for() {
    local what=$1 predicate=$2
    for _ in {1..60}; do
        if "$predicate"; then return 0; fi
        sleep 2
    done
    echo "Timeout: $what" >&2
    return 1
}
old_relay_gone() { [[ -z $(docker ps -aq --filter "label=minikube-cloud-lab.service-uid=$old_uid" --filter "label=minikube-cloud-lab.network=$TEST_NETWORK") ]]; }
lb_two_rebound() { kubectl -n "$namespace" get service mcl-test-lb-two -o json | jq -e '.metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | .[0].hostPort == 30081' > /dev/null; }
conflict_reported() { kubectl -n "$namespace" get service mcl-test-conflict -o json | jq -e '.metadata.annotations | .["minikube-cloud-lab.io/exposure-error"] != null and .["minikube-cloud-lab.io/host-endpoints"] == null' > /dev/null; }
baseline_restored() { relay_ids | sort > "$TEST_ARTIFACT_DIR/current.ids" && cmp -s "$TEST_ARTIFACT_DIR/baseline.ids" "$TEST_ARTIFACT_DIR/current.ids"; }
wait_endpoint() {
    local name=$1
    for _ in {1..90}; do
        if kubectl -n "$namespace" get service "$name" -o json | jq -e \
            '. as $s | .metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | length > 0 and all(.[]; . as $p | any($s.spec.ports[]; .nodePort == $p.nodePort and .port == $p.servicePort) and ($s.spec.type != "NodePort" or .hostPort == .nodePort))' > /dev/null 2>&1; then return 0; fi
        sleep 2
    done
    kubectl -n "$namespace" get service "$name" -o yaml
    return 1
}
check_http() {
    local name=$1 port ip
    wait_endpoint "$name"
    port=$(kubectl -n "$namespace" get service "$name" -o json | jq -r '.metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | .[0].hostPort')
    curl --retry 10 --retry-connrefused --retry-delay 1 --max-time 15 -fsS "http://127.0.0.1:$port" | grep -F 'Welcome to nginx' > /dev/null
    if [[ "$name" == mcl-test-lb-* ]]; then
        ip=$(kubectl -n "$namespace" get service "$name" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
        docker exec "$TEST_CLOUD_CONTAINER" curl --retry 10 --retry-connrefused --max-time 15 -fsS "http://$ip:80" | grep -F 'Welcome to nginx' > /dev/null
    fi
}
snapshot() {
    local ids
    ids=$(relay_ids)
    test -n "$ids"
    # The IDs originate only from our exact ownership filters.
    docker inspect $ids | jq -S 'sort_by(.Id) | map({id:.Id,ip:.NetworkSettings.Networks,ports:.NetworkSettings.Ports})'
}
case ${1:?prepare, check, mutate-cleanup or deleted} in
    prepare)
        namespace=$(kubectl create -f - -o jsonpath='{.metadata.name}' <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
  generateName: service-demo-
YAML
)
        printf '%s\n' "$namespace" > "$namespace_file"
        relay_ids | sort > "$TEST_ARTIFACT_DIR/baseline.ids"
        kubectl -n "$namespace" create deployment web --image=nginx:1.28-alpine --port=80
        kubectl -n "$namespace" rollout status deployment/web --timeout=3m
        for name in "${services[@]}"; do
            kind=LoadBalancer
            if [[ "$name" == mcl-test-node ]]; then kind=NodePort; fi
            kubectl -n "$namespace" expose deployment web --name="$name" --type="$kind" --port=80 --target-port=80
        done
        kubectl -n "$namespace" patch service mcl-test-node --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30080}]'
        for name in "${services[@]}"; do check_http "$name"; done
        ids=$(relay_ids)
        docker inspect $ids > "$TEST_ARTIFACT_DIR/relays.json"
        docker network inspect "$TEST_NETWORK" > "$TEST_ARTIFACT_DIR/network.json"
        python3 - "$TEST_ARTIFACT_DIR" "$TEST_NETWORK" <<'PY'
import ipaddress,json,sys
from pathlib import Path
p=Path(sys.argv[1]);network=json.loads((p/'network.json').read_text())[0]
subnet=ipaddress.ip_network(network['IPAM']['Config'][0]['Subnet']);assert subnet.prefixlen==23
first,second=list(subnet.subnets(new_prefix=24))
relays=json.loads((p/'relays.json').read_text());assert len(relays)==3+len((p/'baseline.ids').read_text().splitlines()),len(relays)
for relay in relays:
    assert ipaddress.ip_address(relay['NetworkSettings']['Networks'][sys.argv[2]]['IPAddress']) in second
    assert not relay['Mounts']
    for bindings in relay['NetworkSettings']['Ports'].values():
        assert len(bindings)==1 and bindings[0]['HostIp']=='127.0.0.1',bindings
relay_ids={r['Id'] for r in relays}
for key,member in network['Containers'].items():
    if key not in relay_ids:
        ip=ipaddress.ip_interface(member['IPv4Address']).ip
        assert ip in first or ip==second.network_address,member
print('Pool separation, four relays, localhost-only bindings and no mounts PASS')
PY
        snapshot > "$TEST_ARTIFACT_DIR/before.json"
        docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}' $ids
        ;;
    check)
        for name in "${services[@]}"; do check_http "$name"; done
        snapshot > "$TEST_ARTIFACT_DIR/after.json"
        diff -u "$TEST_ARTIFACT_DIR/before.json" "$TEST_ARTIFACT_DIR/after.json"
        echo 'Relay IDs and endpoints survived cloud restart/capacity changes'
        ;;
    mutate-cleanup)
        old_uid=$(kubectl -n "$namespace" get service mcl-test-node -o jsonpath='{.metadata.uid}')
        kubectl -n "$namespace" delete service mcl-test-node
        wait_for "relay of deleted NodePort Service removed" old_relay_gone
        kubectl -n "$namespace" expose deployment web --name=mcl-test-node --type=NodePort --port=80 --target-port=80
        kubectl -n "$namespace" patch service mcl-test-node --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30080}]'
        check_http mcl-test-node
        test "$(kubectl -n "$namespace" get service mcl-test-node -o jsonpath='{.metadata.uid}')" != "$old_uid"
        kubectl -n "$namespace" patch service mcl-test-lb-two --type=merge -p='{"spec":{"type":"NodePort","ports":[{"name":"http","port":8081,"targetPort":80,"nodePort":30081,"protocol":"TCP"}]}}'
        wait_for "LoadBalancer converted to NodePort 30081" lb_two_rebound
        curl --retry 10 --retry-connrefused --max-time 15 -fsS http://127.0.0.1:30081 | grep -F 'Welcome to nginx' > /dev/null
        image=$(docker inspect "$TEST_CLOUD_CONTAINER" --format '{{.Image}}')
        blocker=$(docker run -d --entrypoint cloud -p 127.0.0.1:30090:30090 "$image" service-proxy --config-json '[{"listenPort":30090,"target":"127.0.0.1:1"}]')
        trap 'docker rm -f "$blocker" > /dev/null' EXIT
        kubectl -n "$namespace" expose deployment web --name=mcl-test-conflict --type=NodePort --port=80 --target-port=80
        kubectl -n "$namespace" patch service mcl-test-conflict --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30090}]'
        wait_for "occupied NodePort reported as exposure error" conflict_reported
        kubectl -n "$namespace" get service mcl-test-conflict -o json | jq -e '.metadata.annotations["minikube-cloud-lab.io/exposure-error"] | test("port|bind";"i")'
        docker rm -f "$blocker" > /dev/null
        trap - EXIT
        check_http mcl-test-conflict
        kubectl -n "$namespace" delete service "${services[@]}" mcl-test-conflict
        wait_for "pre-existing relays restored" baseline_restored
        kubectl delete namespace "$namespace"
        echo 'UID replacement, port/type changes, occupied NodePort recovery and Service cleanup PASS'
        ;;
    deleted)
        test -z "$(relay_ids)"
        echo 'Cluster relay cleanup PASS'
        ;;
    *) echo 'Unknown test phase' >&2; exit 1 ;;
esac
