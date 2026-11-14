#!/usr/bin/env bash
# Feasibility probe for standalone instances: two kicbase system containers started
# with the flags the cloud uses, checked for systemd, SSH, nested Docker, networking,
# persistence and cgroup limits. Removes everything it creates.
set -euo pipefail
RUNTIME=${RUNTIME:-docker}
KICBASE_IMAGE=${KICBASE_IMAGE:-gcr.io/k8s-minikube/kicbase:v0.0.51@sha256:4a1c825b61479e6c898851ea66f13c620aaeab6002746e95067fc2c4b38a0b24}
FEAS_SUBNET=${FEAS_SUBNET:-172.30.8.0/23}
net=mcl-feasibility
base=${FEAS_SUBNET%.*/*}
ip_a=$base.200 ip_b=$base.201
case $RUNTIME in
docker) apparmor=(--security-opt apparmor=unconfined) exec_opt= ;;
podman) apparmor=() exec_opt=:exec ;;
*) echo "RUNTIME must be docker or podman" >&2; exit 2 ;;
esac

for n in a b; do
    if $RUNTIME container inspect "mcl-vm-$n" >/dev/null 2>&1 || $RUNTIME volume inspect "mcl-vm-$n" >/dev/null 2>&1; then
        echo "mcl-vm-$n already exists; remove it before running the probe" >&2; exit 2
    fi
done
if $RUNTIME network inspect "$net" >/dev/null 2>&1; then
    echo "network $net already exists; remove it before running the probe" >&2; exit 2
fi
cleanup() {
    $RUNTIME rm -f mcl-vm-a mcl-vm-b >/dev/null 2>&1 || true
    $RUNTIME volume rm -f mcl-vm-a mcl-vm-b >/dev/null 2>&1 || true
    $RUNTIME network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "FAIL $1: $2" >&2; exit 1; }
pass() { echo "PASS $1"; }
ex() { local n=$1; shift; $RUNTIME exec "mcl-vm-$n" "$@"; }
booted() {
    local state
    # The entrypoint prepares mounts before exec'ing systemd, which reports offline meanwhile.
    for _ in $(seq 60); do
        state=$(ex "$1" timeout 180 systemctl is-system-running --wait 2>&1 || true)
        [ "$state" = offline ] || break
        sleep 1
    done
    case $state in
    running) ;;
    degraded) echo "degraded units in $1:"; ex "$1" systemctl --failed --no-legend || true ;;
    *) fail "systemd in $1" "state '$state'" ;;
    esac
}

$RUNTIME network create --subnet "$FEAS_SUBNET" --gateway "$base.1" "$net" >/dev/null
for pair in "a $ip_a" "b $ip_b"; do
    read -r n ip <<<"$pair"
    $RUNTIME volume create "mcl-vm-$n" >/dev/null
    $RUNTIME run -d -t --name "mcl-vm-$n" --hostname "$n" --privileged \
        --security-opt seccomp=unconfined "${apparmor[@]}" \
        --tmpfs /tmp --tmpfs /run -v /lib/modules:/lib/modules:ro \
        -v "mcl-vm-$n:/var$exec_opt" -e "container=$RUNTIME" \
        --cpu-period 100000 --cpu-quota 200000 --memory 2048m --memory-swap 2048m --pids-limit 4096 \
        --stop-signal SIGRTMIN+3 --network "$net" --ip "$ip" \
        --label "minikube-cloud-lab.instance=$n" "$KICBASE_IMAGE" >/dev/null
done

booted a
booted b
pass "systemd running"

ex b bash -c "</dev/tcp/$ip_a/22" || fail "ssh port" "b cannot reach $ip_a:22"
ex a sudo -n -u docker sudo -n true || fail "sudo" "user docker has no passwordless sudo"
pass "sshd reachable and passwordless sudo"

ex a docker run --rm hello-world | grep -q 'Hello from Docker!' || fail "nested docker" "hello-world did not run"
ex a docker run -d --name web -p 8080:80 -v webdata:/usr/share/nginx/html nginx:1.27-alpine >/dev/null
for _ in $(seq 30); do ex b curl -fsS -o /dev/null "http://$ip_a:8080" && break; sleep 2; done
ex b curl -fsS -o /dev/null "http://$ip_a:8080" || fail "nested docker service" "b cannot reach nginx on $ip_a:8080"
pass "nested Docker with a published long-lived container"

ex a bash -c "</dev/tcp/$ip_b/22" || fail "instance network" "a cannot reach $ip_b:22"
ex a curl -fsS -o /dev/null https://deb.debian.org || fail "internet" "a cannot reach https://deb.debian.org"
pass "instance network, DNS and Internet"

ex a sh -c 'echo persisted > /home/docker/persist'
started=$(date +%s)
$RUNTIME stop -t 30 mcl-vm-a >/dev/null
elapsed=$(($(date +%s) - started))
[ "$elapsed" -lt 30 ] || fail "clean stop" "stop took ${elapsed}s (systemd ignored the stop signal)"
$RUNTIME start mcl-vm-a >/dev/null
booted a
[ "$(ex a cat /home/docker/persist)" = persisted ] || fail "persistence" "/home/docker/persist lost"
ex a docker image inspect hello-world >/dev/null || fail "persistence" "Docker image in /var lost"
pass "clean stop in ${elapsed}s and persistence across stop/start"

[ "$(ex a cat /sys/fs/cgroup/cpu.max)" = "200000 100000" ] || fail "cpu limit" "cpu.max=$(ex a cat /sys/fs/cgroup/cpu.max)"
[ "$(ex a cat /sys/fs/cgroup/memory.max)" = 2147483648 ] || fail "memory limit" "memory.max=$(ex a cat /sys/fs/cgroup/memory.max)"
[ "$(ex a cat /sys/fs/cgroup/pids.max)" = 4096 ] || fail "pids limit" "pids.max=$(ex a cat /sys/fs/cgroup/pids.max)"
pass "cgroup CPU, memory and PID limits"

echo "MACHINE-ID a=$(ex a cat /etc/machine-id) b=$(ex b cat /etc/machine-id)"
echo "ALL CHECKS PASSED"
