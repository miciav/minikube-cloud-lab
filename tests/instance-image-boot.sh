#!/usr/bin/env bash
# Boots two containers of a standalone instance image with the cloud's container settings
# and checks systemd, sshd at boot, per-instance SSH host keys and that an SSH login
# gets a systemd user session (rootful Docker).
set -euo pipefail
image=${1:?usage: instance-image-boot.sh IMAGE [SSH_USER]}
user=${2:-ubuntu}
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
docker exec mcl-boot-b ssh-keygen -q -t ed25519 -N "" -f /root/probe
public=$(docker exec mcl-boot-b cat /root/probe.pub)
docker exec -e KEY="$public" -e USER_NAME="$user" mcl-boot-a sh -c 'install -d -m 700 -o "$USER_NAME" -g "$USER_NAME" "/home/$USER_NAME/.ssh" &&
    printf "%s\n" "$KEY" > "/home/$USER_NAME/.ssh/authorized_keys" && chown "$USER_NAME:" "/home/$USER_NAME/.ssh/authorized_keys"'
session=$(docker exec mcl-boot-b ssh -i /root/probe -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "$user@mcl-boot-a" 'echo "runtime=$XDG_RUNTIME_DIR"; systemctl --user is-system-running' 2>&1 || true)
case $session in
*runtime=/run/user/*running* | *runtime=/run/user/*degraded* | *runtime=/run/user/*starting*) ;;
*) fail "SSH login has no systemd user session: $session" ;;
esac
echo "PASS $image boots with sshd, per-instance host keys and SSH user sessions"
