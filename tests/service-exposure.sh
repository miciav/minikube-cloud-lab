#!/usr/bin/env bash
# Live checks shared by Docker Desktop and the Linux student workflow.
set -euo pipefail
: "${KUBECONFIG:?use an isolated lab kubeconfig}"
: "${TEST_CLUSTER:?managed test cluster}"
: "${TEST_NETWORK:?dedicated Docker network}"
: "${TEST_CLOUD_CONTAINER:?cloud container ID}"
: "${TEST_ARTIFACT_DIR:?directory for endpoint snapshots}"
mkdir -p "$TEST_ARTIFACT_DIR"
services=(mcl-test-node mcl-test-lb-one mcl-test-lb-two)
relay_ids() {
    docker ps -aq --filter label=minikube-cloud-lab.service-proxy=true \
        --filter "label=minikube-cloud-lab.cluster=$TEST_CLUSTER" \
        --filter "label=minikube-cloud-lab.network=$TEST_NETWORK"
}
wait_endpoint() {
    local name=$1
    for _ in {1..90}; do
        if kubectl -n yakd-dashboard get service "$name" -o json | jq -e \
            '. as $s | .metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | length > 0 and all(.[]; . as $p | any($s.spec.ports[]; .nodePort == $p.nodePort and .port == $p.servicePort) and ($s.spec.type != "NodePort" or .hostPort == .nodePort))' > /dev/null 2>&1; then return 0; fi
        sleep 2
    done
    kubectl -n yakd-dashboard get service "$name" -o yaml
    return 1
}
check_http() {
    local name=$1 port ip
    wait_endpoint "$name"
    port=$(kubectl -n yakd-dashboard get service "$name" -o json | jq -r '.metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | .[0].hostPort')
    curl --retry 10 --retry-connrefused --retry-delay 1 --max-time 15 -fsS "http://127.0.0.1:$port" | grep -i '<html' > /dev/null
    if [[ "$name" == mcl-test-lb-* ]]; then
        ip=$(kubectl -n yakd-dashboard get service "$name" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
        docker exec "$TEST_CLOUD_CONTAINER" curl --retry 10 --retry-connrefused --max-time 15 -fsS "http://$ip:80" | grep -i '<html' > /dev/null
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
        for name in "${services[@]}"; do
            kind=LoadBalancer
            if [[ "$name" == mcl-test-node ]]; then kind=NodePort; fi
            kubectl -n yakd-dashboard expose deployment yakd-dashboard --name="$name" --type="$kind" --port=80 --target-port=8080
        done
        kubectl -n yakd-dashboard patch service mcl-test-node --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30080}]'
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
relays=json.loads((p/'relays.json').read_text());assert len(relays)==4,len(relays)
for relay in relays:
    assert ipaddress.ip_address(relay['NetworkSettings']['Networks'][sys.argv[2]]['IPAddress']) in second
    assert not relay['Mounts']
    for bindings in relay['NetworkSettings']['Ports'].values():
        assert len(bindings)==1 and bindings[0]['HostIp']=='127.0.0.1',bindings
relay_ids={r['Id'] for r in relays}
for key,member in network['Containers'].items():
    if key not in relay_ids: assert ipaddress.ip_interface(member['IPv4Address']).ip in first,member
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
        old_uid=$(kubectl -n yakd-dashboard get service mcl-test-node -o jsonpath='{.metadata.uid}')
        kubectl -n yakd-dashboard delete service mcl-test-node
        for _ in {1..60}; do
            if [[ -z $(docker ps -aq --filter "label=minikube-cloud-lab.service-uid=$old_uid" --filter "label=minikube-cloud-lab.network=$TEST_NETWORK") ]]; then break; fi
            sleep 2
        done
        kubectl -n yakd-dashboard expose deployment yakd-dashboard --name=mcl-test-node --type=NodePort --port=80 --target-port=8080
        kubectl -n yakd-dashboard patch service mcl-test-node --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30080}]'
        check_http mcl-test-node
        test "$(kubectl -n yakd-dashboard get service mcl-test-node -o jsonpath='{.metadata.uid}')" != "$old_uid"
        kubectl -n yakd-dashboard patch service mcl-test-lb-two --type=merge -p='{"spec":{"type":"NodePort","ports":[{"name":"http","port":8081,"targetPort":8080,"nodePort":30081,"protocol":"TCP"}]}}'
        for _ in {1..60}; do
            if kubectl -n yakd-dashboard get service mcl-test-lb-two -o json | jq -e '.metadata.annotations["minikube-cloud-lab.io/host-endpoints"] | fromjson | .[0].hostPort == 30081' > /dev/null; then break; fi
            sleep 2
        done
        curl --retry 10 --retry-connrefused --max-time 15 -fsS http://127.0.0.1:30081 | grep -i '<html' > /dev/null
        image=$(docker inspect "$TEST_CLOUD_CONTAINER" --format '{{.Image}}')
        blocker=$(docker run -d --entrypoint cloud -p 127.0.0.1:30090:30090 "$image" service-proxy --config-json '[{"listenPort":30090,"target":"127.0.0.1:1"}]')
        trap 'docker rm -f "$blocker" > /dev/null' EXIT
        kubectl -n yakd-dashboard expose deployment yakd-dashboard --name=mcl-test-conflict --type=NodePort --port=80 --target-port=8080
        kubectl -n yakd-dashboard patch service mcl-test-conflict --type=json -p='[{"op":"replace","path":"/spec/ports/0/nodePort","value":30090}]'
        for _ in {1..60}; do
            if kubectl -n yakd-dashboard get service mcl-test-conflict -o json | jq -e '.metadata.annotations | .["minikube-cloud-lab.io/exposure-error"] != null and .["minikube-cloud-lab.io/host-endpoints"] == null' > /dev/null; then break; fi
            sleep 2
        done
        kubectl -n yakd-dashboard get service mcl-test-conflict -o json | jq -e '.metadata.annotations["minikube-cloud-lab.io/exposure-error"] | test("port|bind";"i")'
        docker rm -f "$blocker" > /dev/null
        trap - EXIT
        check_http mcl-test-conflict
        kubectl -n yakd-dashboard delete service "${services[@]}" mcl-test-conflict
        for _ in {1..60}; do if [[ $(relay_ids | wc -l | tr -d ' ') == 1 ]]; then break; fi; sleep 2; done
        test "$(relay_ids | wc -l | tr -d ' ')" = 1
        echo 'UID replacement, port/type changes, occupied NodePort recovery and Service cleanup PASS'
        ;;
    deleted)
        test -z "$(relay_ids)"
        echo 'Cluster relay cleanup PASS'
        ;;
    *) echo 'Unknown test phase' >&2; exit 1 ;;
esac
