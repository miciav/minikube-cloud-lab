#!/usr/bin/env bash
# Boots two containers of a standalone instance image with the cloud's container settings
# and checks systemd, sshd at boot and per-instance SSH host keys (rootful Docker).
set -euo pipefail
image=${1:?usage: instance-image-boot.sh IMAGE}
net=mcl-image-boot
cleanup() {
    docker rm -f mcl-boot-a mcl-boot-b >/dev/null 2>&1 || true
    docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
fail() { echo "FAIL $1" >&2; docker logs --tail 40 "mcl-boot-${2:-a}" >&2 || true; exit 1; }
docker network create "$net" >/dev/null
for n in a b; do
    docker run -d -t --name "mcl-boot-$n" --privileged --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
        --tmpfs /tmp --tmpfs /run -v /lib/modules:/lib/modules:ro -e container=docker \
        --stop-signal SIGRTMIN+3 --network "$net" "$image" >/dev/null
done
for n in a b; do
    state=
    for _ in $(seq 90); do
        state=$(docker exec "mcl-boot-$n" systemctl is-system-running 2>&1 || true)
        case $state in running | degraded) break ;; esac
        sleep 1
    done
    case $state in running | degraded) ;; *) fail "systemd in $n: $state" "$n" ;; esac
done
docker exec mcl-boot-b bash -c '</dev/tcp/mcl-boot-a/22' || fail "sshd is not listening at boot"
key() { docker exec "mcl-boot-$1" cat /etc/ssh/ssh_host_ed25519_key.pub | cut -d' ' -f2; }
[ "$(key a)" != "$(key b)" ] || fail "instances share SSH host keys"
echo "PASS $image boots with sshd and per-instance host keys"
