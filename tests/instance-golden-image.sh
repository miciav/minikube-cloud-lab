#!/usr/bin/env bash
# Capture a debian-12 instance with inner Docker data as a golden image, then launch clones and a
# group from it. Run with the cloud and instance images built from the same checkout.
set -euo pipefail
runtime=${RUNTIME:-docker}
if [[ -n ${TEST_CLOUD_CONTAINER:-} ]]; then
    cloud() { "$runtime" exec -i "$TEST_CLOUD_CONTAINER" cloud "$@"; }
else
    export CLOUD_RUNTIME="$runtime"
    source "$(dirname -- "${BASH_SOURCE[0]}")/../scripts/configure.sh"
    cloud() { labcompose exec -T cloud cloud "$@"; }
fi
base=${CLOUD_TEST_API_URL:-http://127.0.0.1:8080}
id="gold-$(date +%s)-$$"
image=$id busy=$id-busy src=$id-src clone1=$id-c1 clone2=$id-c2 group=$id-g
scratch=$(mktemp -d)
cleanup() {
    for _ in {1..60}; do if capture_done; then break; fi; sleep 2; done
    cloud groups delete "$group" </dev/null >/dev/null 2>&1 || true
    for vm in "$clone2" "$clone1" "$src"; do cloud instances delete "$vm" </dev/null >/dev/null 2>&1 || true; done
    # The group releases the image only once its members are gone.
    for _ in {1..60}; do if group_gone; then break; fi; sleep 2; done
    for name in "$busy" "$image"; do cloud images delete "$name" </dev/null >/dev/null 2>&1 || true; done
    rm -rf "$scratch"
}
trap cleanup EXIT
must_reject() {
    local expected=$1 output
    shift
    if output=$(cloud "$@" </dev/null 2>&1); then echo "Unexpected success: $*" >&2; return 1; fi
    [[ "$output" == *"$expected"* ]] || { echo "$output" >&2; return 1; }
}
guest() { local vm=$1; shift; cloud instances ssh "$vm" -- "$@" </dev/null; }
root() { cloud instances ssh "$1" -- sudo sh -eu; } # script on stdin
field() { # URL KEY
    curl -fsS "$base/v1/$1" | python3 -c 'import json, sys; print(json.load(sys.stdin)[sys.argv[1]])' "$2"
}
image_status() { cloud images list </dev/null | awk -v n="$1" '$1 == n {print $4}'; }
wait_for() {
    local n
    for ((n = 0; n < 180; n++)); do
        if "$@"; then return; fi
        sleep 2
    done
    echo "Timed out waiting for $*" >&2
    return 1
}
capture_done() { [[ $(image_status "$busy") != CREATING ]]; }
group_ready() { [[ $(field "groups/$group" ready 2>/dev/null) == 1 ]]; }
group_gone() { [[ $(curl -fsS "$base/v1/groups") != *"\"$group\""* ]]; }
inner_ready() { [[ $(guest "$1" docker inspect -f '{{.State.Running}}' web 2>/dev/null) == true ]]; }
ssh_as_docker() { # VM KEYFILE: SSH over the guest loopback with a given private key
    "$runtime" exec -i "mcl-vm-$1" sh -eu -c '
        key=$(mktemp)
        trap '\''rm -f "$key"'\'' EXIT
        cat > "$key"
        ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -i "$key" docker@localhost true
    ' < "$2"
}

ssh-keygen -q -t ed25519 -N '' -f "$scratch/student"
cloud instances create "$src" --image debian-12
root "$src" <<SCRIPT
echo golden > /etc/golden-marker
useradd -m mario
echo '$(cat "$scratch/student.pub")' >> /home/docker/.ssh/authorized_keys
until docker info >/dev/null 2>&1; do sleep 1; done
docker pull -q nginx:1.29-alpine
docker image inspect -f '{{.Id}}' nginx:1.29-alpine > /etc/golden-nginx-id
docker run -d --restart=always --name web -p 8080:80 nginx:1.29-alpine \
    sh -c 'echo ok > /usr/share/nginx/html/health && exec nginx -g "daemon off;"' >/dev/null
SCRIPT
field "instances/$src/ssh" privateKey > "$scratch/source-key"
field "instances/$src/ssh" hostKey > "$scratch/api-src"
guest "$src" cat /etc/ssh/ssh_host_ed25519_key.pub > "$scratch/host-src"
guest "$src" cat /etc/machine-id > "$scratch/id-src"
cloud instances stop "$src"

cloud images create "$image" --from "$src" --description "nginx ready" | grep -Fx "Created image $image"
cloud images list | grep -E "^$image +instance/$src +debian-12 +AVAILABLE .*nginx ready$"
cloud images create "$busy" --from "$src" --async >/dev/null
must_reject 'being captured' instances start "$src"
wait_for capture_done
test "$(image_status "$busy")" = AVAILABLE
cloud images delete "$busy"
cloud instances delete "$src"

for vm in "$clone1" "$clone2"; do
    cloud instances create "$vm" --image "$image"
    if [[ $vm == "$clone1" ]]; then must_reject 'used by an instance or group' images delete "$image"; fi
    guest "$vm" test -f /etc/golden-marker
    guest "$vm" id mario
    guest "$vm" grep -qF "'$(cut -d' ' -f2 "$scratch/student.pub")'" /home/docker/.ssh/authorized_keys
    wait_for inner_ready "$vm"
    guest "$vm" 'test "$(docker image inspect -f {{.Id}} nginx:1.29-alpine)" = "$(cat /etc/golden-nginx-id)"'
    guest "$vm" docker run --pull=never --rm nginx:1.29-alpine nginx -v
    guest "$vm" curl -fsS localhost:8080/health | grep -Fx ok
    field "instances/$vm/ssh" hostKey > "$scratch/api-$vm"
    guest "$vm" cat /etc/ssh/ssh_host_ed25519_key.pub > "$scratch/host-$vm"
    guest "$vm" cat /etc/machine-id > "$scratch/id-$vm"
    if ssh_as_docker "$vm" "$scratch/source-key" 2>/dev/null; then echo "The source key still logs in to $vm" >&2; exit 1; fi
    ssh_as_docker "$vm" "$scratch/student"
done
for kind in api host id; do
    test "$(sort -u "$scratch/$kind"-* | wc -l)" -eq 3 || { head "$scratch/$kind"-* >&2; exit 1; }
done

if [[ $(field instance-autoscaling state) == active ]]; then
    cloud groups create "$group" --image "$image" --desired 1 --max 1
    wait_for group_ready
    cloud groups delete "$group"
    wait_for group_gone
else
    echo "SKIP: group from $image (run lab install instance-autoscaling first)"
fi

# Personal images are local: the cloud must never try to pull one.
if [[ -n ${TEST_CLOUD_CONTAINER:-} ]]; then
    "$runtime" logs "$TEST_CLOUD_CONTAINER" > "$scratch/cloud.log" 2>&1
else
    labcompose logs cloud > "$scratch/cloud.log" 2>&1 # podman-compose 1.5 has no --no-color
fi
grep -F "operation started" "$scratch/cloud.log" | grep -qF "target=$clone1"
if grep -F "pulling instance image" "$scratch/cloud.log" | grep -F "image-$image:"; then
    echo "The cloud pulled personal image $image" >&2
    exit 1
fi

for vm in "$clone1" "$clone2"; do cloud instances delete "$vm"; done
cloud images delete "$image" | grep -Fx "Deleted image $image"
test -z "$("$runtime" volume ls -q --filter "name=mcl-image-$image-var")"
echo "PASS: golden image capture, clones with inner Docker data and fresh identity, guarded deletion"
