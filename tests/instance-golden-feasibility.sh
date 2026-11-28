#!/usr/bin/env bash
# Feasibility probe for golden images: commit a stopped kicbase instance without /var,
# copy its /var volume to an image volume and from there to a new instance volume with
# privileged helpers, then check that the clone keeps files, users and inner Docker data.
# Removes everything it creates.
set -euo pipefail
RUNTIME=${RUNTIME:-docker}
KICBASE_IMAGE=${KICBASE_IMAGE:-gcr.io/k8s-minikube/kicbase:v0.0.51@sha256:4a1c825b61479e6c898851ea66f13c620aaeab6002746e95067fc2c4b38a0b24}
image=mcl-golden-probe:rootfs
image_var=mcl-image-gprobe-var
helper=mcl-golden-probe-helper
case $RUNTIME in
docker) apparmor=(--security-opt apparmor=unconfined) exec_opt= ;;
podman) apparmor=() exec_opt=:exec ;;
*) echo "RUNTIME must be docker or podman" >&2; exit 2 ;;
esac
security=(--privileged --security-opt seccomp=unconfined "${apparmor[@]}")

for n in mcl-vm-gsrc mcl-vm-gclone $helper; do
    if $RUNTIME container inspect "$n" >/dev/null 2>&1; then
        echo "container $n already exists; remove it before running the probe" >&2; exit 2
    fi
done
for v in mcl-vm-gsrc mcl-vm-gclone $image_var; do
    if $RUNTIME volume inspect "$v" >/dev/null 2>&1; then
        echo "volume $v already exists; remove it before running the probe" >&2; exit 2
    fi
done
if $RUNTIME image inspect "$image" >/dev/null 2>&1; then
    echo "image $image already exists; remove it before running the probe" >&2; exit 2
fi
cleanup() {
    $RUNTIME rm -f mcl-vm-gsrc mcl-vm-gclone $helper >/dev/null 2>&1 || true
    $RUNTIME volume rm -f mcl-vm-gsrc mcl-vm-gclone $image_var >/dev/null 2>&1 || true
    $RUNTIME rmi -f "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "FAIL $1: $2" >&2; exit 1; }
pass() { echo "PASS $1"; }
ex() { local n=$1; shift; $RUNTIME exec "mcl-vm-$n" "$@"; }
booted() {
    local state
    # The entrypoint runs before systemd, and D-Bus starts after it: systemctl reports
    # offline, then "Failed to connect to bus", for a few seconds.
    for _ in $(seq 60); do
        state=$(ex "$1" timeout 180 systemctl is-system-running --wait 2>&1 || true)
        case $state in offline | "Failed to connect to bus"*) sleep 1 ;; *) break ;; esac
    done
    case $state in
    running) ;;
    degraded) echo "degraded units in $1:"; ex "$1" systemctl --failed --no-legend || true ;;
    *) fail "systemd in $1" "state '$state'" ;;
    esac
}
instance() { # name image
    $RUNTIME run -d -t --name "mcl-vm-$1" --hostname "$1" "${security[@]}" \
        --tmpfs /tmp --tmpfs /run -v /lib/modules:/lib/modules:ro \
        -v "mcl-vm-$1:/var$exec_opt" -e "container=$RUNTIME" \
        --cpu-period 100000 --cpu-quota 200000 --memory 2048m --memory-swap 2048m --pids-limit 4096 \
        --stop-signal SIGRTMIN+3 --label "minikube-cloud-lab.instance=$1" "$2" >/dev/null
}
copy_volume() { # source target
    $RUNTIME run -d --name $helper "${security[@]}" --network none --stop-signal SIGKILL --entrypoint sleep \
        -v "$1:/mnt/source-var:ro" -v "$2:/var$exec_opt" "$image" infinity >/dev/null
    $RUNTIME exec $helper cp -a /mnt/source-var/. /var/ || fail "volume copy" "cp -a $1 -> $2 failed"
    $RUNTIME rm -f $helper >/dev/null
}

$RUNTIME volume create mcl-vm-gsrc >/dev/null
instance gsrc "$KICBASE_IMAGE"
booted gsrc
ex gsrc sh -c 'docker pull -q nginx:1.29-alpine && docker run -d --name web -p 8080:80 nginx:1.29-alpine' >/dev/null
ex gsrc sh -c 'echo golden > /etc/golden-marker && echo golden > /var/lib/golden-marker && useradd -m mario'
image_id=$(ex gsrc docker image inspect -f '{{.Id}}' nginx:1.29-alpine)
driver=$(ex gsrc docker info --format '{{.Driver}}')
echo "inner Docker storage driver: $driver"
pass "source instance prepared"

$RUNTIME stop -t 30 mcl-vm-gsrc >/dev/null
$RUNTIME commit mcl-vm-gsrc "$image" >/dev/null
$RUNTIME volume create $image_var >/dev/null
copy_volume mcl-vm-gsrc $image_var
$RUNTIME volume create mcl-vm-gclone >/dev/null
copy_volume $image_var mcl-vm-gclone
pass "rootfs committed and /var copied source -> image -> clone"

instance gclone "$image"
booted gclone
pass "clone systemd running"

ex gclone test -f /etc/golden-marker || fail "rootfs file" "/etc/golden-marker missing"
ex gclone test -f /var/lib/golden-marker || fail "/var file" "/var/lib/golden-marker missing"
ex gclone id mario >/dev/null || fail "user" "mario missing"
pass "files and user preserved"

clone_id=$(ex gclone docker image inspect -f '{{.Id}}' nginx:1.29-alpine) || fail "inner image" "nginx:1.29-alpine missing"
[ "$clone_id" = "$image_id" ] || fail "inner image" "ID $clone_id differs from source $image_id"
ex gclone docker start web >/dev/null || fail "inner container" "docker start web failed"
for _ in $(seq 30); do ex gclone curl -fsS -o /dev/null localhost:8080 && break; sleep 1; done
ex gclone curl -fsS -o /dev/null localhost:8080 || fail "inner service" "nginx on localhost:8080 unreachable"
pass "inner Docker image and container preserved without a pull"
echo "ALL CHECKS PASSED"
