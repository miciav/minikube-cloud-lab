#!/usr/bin/env bash
# Provision a dedicated Multipass VM and verify the committed checkout as a rootless user.
set -euo pipefail
vm=mcl-rootless-test
full=false
usage() { echo "Usage: $0 [--vm NAME] [--full]"; }
while (( $# )); do
    case $1 in
        --vm) vm=${2:?missing VM name}; shift 2 ;;
        --full) full=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
[[ $vm =~ ^[a-z][a-z0-9-]*$ ]] || { echo 'Invalid VM name' >&2; exit 2; }
root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
revision=$(git -C "$root" rev-parse HEAD)
echo "Testing $revision in $vm"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
if ! multipass info "$vm" >/dev/null 2>&1; then
    cat > "$scratch/cloud-init.yaml" <<'CLOUD'
#cloud-config
write_files:
  - path: /etc/mcl-multipass-rootless-test
    content: "1\n"
  - path: /etc/systemd/system/user@.service.d/delegate.conf
    content: |
      [Service]
      Delegate=cpu cpuset io memory pids
  - path: /etc/modules-load.d/cloud-lab.conf
    content: |
      br_netfilter
      vxlan
CLOUD
    multipass launch 26.04 --name "$vm" --cpus 4 --memory 8G --disk 48G --cloud-init "$scratch/cloud-init.yaml"
else
    multipass start "$vm"
fi
guest() { multipass exec "$vm" -- "$@"; }
guest test -f /etc/mcl-multipass-rootless-test || { echo "Refusing to modify an unrelated VM: $vm" >&2; exit 1; }
guest bash -se <<'PROVISION'
if ! command -v podman >/dev/null || ! command -v podman-compose >/dev/null; then
    sudo apt-get update
    sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y podman podman-compose uidmap dbus-user-session fuse-overlayfs slirp4netns
fi
# Ubuntu's pasta AppArmor profile denies the termination signal from Podman at teardown.
mkdir -p "$HOME/.config/containers/containers.conf.d"
printf '%s\n' '[network]' 'default_rootless_network_cmd="slirp4netns"' > "$HOME/.config/containers/containers.conf.d/90-mcl-test.conf"
sudo modprobe br_netfilter
sudo modprobe vxlan
sudo loginctl enable-linger "$(id -un)"
systemctl --user enable --now podman.socket
test "$(id -u)" != 0
podman info --format 'Podman {{.Version.Version}} rootless={{.Host.Security.Rootless}} cgroups={{.Host.CgroupsVersion}}'
test "$(podman info --format '{{.Host.Security.Rootless}}')" = true
PROVISION
git -C "$root" archive --format=tar.gz -o "$scratch/source.tgz" "$revision"
guest_archive=$(guest mktemp /tmp/mcl-source-XXXXXXXX.tgz)
multipass transfer "$scratch/source.tgz" "$vm:$guest_archive"
guest bash -se -- "$revision" "$full" "$guest_archive" <<'VERIFY'
set -o pipefail
revision=$1
full=$2
mkdir -p "$HOME/mcl-test"
cd "$HOME/mcl-test"
exec 9>.lock
flock -n 9 || { echo "Another verification is running" >&2; exit 1; }
mkdir -p "$revision"
cd "$revision"
tar xzf "$3"
rm -f "$3"
export CLOUD_RUNTIME=podman COMPOSE_PROJECT_NAME=mcl-rootless-verification
export CLOUD_IMAGE="localhost/mcl-test/cloud:$revision"
build() {
    podman build --target "$1" --format docker -t "$2" .
}
build cloud "$CLOUD_IMAGE"
source scripts/configure.sh
cloud() { labcompose exec -T cloud cloud "$@" </dev/null; }
active=$(labcompose ps -q)
if [[ -n $active ]]; then echo "Test stack already active; inspect it before retrying" >&2; exit 1; fi
instance="probe-$(cat /proc/sys/kernel/random/uuid)"
started=false
created=false
cleanup() {
    local rc=$?
    trap - EXIT
    if ((rc != 0)) && $started; then labcompose logs --tail=40 cloud >&2 || true; fi
    if $created; then cloud instances delete "$instance" || exit 1; fi
    if $started; then labcompose down || exit 1; fi
    exit "$rc"
}
trap cleanup EXIT
started=true
labcompose up -d cloud
for _ in {1..40}; do if cloud images list >/dev/null 2>&1; then break; fi; sleep 1; done
cloud images list
# Check the installed API, including its exact rootless capability boundary.
python3 - <<'PY'
import json
import urllib.error
import urllib.request

for method, path, body in [
    ("GET", "/v1/disks", None),
    ("POST", "/v1/disks/rootless-probe", b'{"size":"64Mi"}'),
]:
    req = urllib.request.Request("http://127.0.0.1:8080" + path, data=body,
                                 method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            raise SystemExit(f"Unexpected {response.status} for {method} {path}")
    except urllib.error.HTTPError as error:
        if error.code != 503 or json.load(error).get("error") != "instance disks require rootful Docker":
            raise
print("PASS: disk requests return the documented rootless error")
PY
created=true
cloud instances create "$instance"
podman exec -i --user 0 "mcl-vm-$instance" bash -se <<'BLOCK'
export LC_ALL=C
dir=$(mktemp -d)
device=
cleanup() {
    if mountpoint -q "$dir/mount"; then umount "$dir/mount" || return 1; fi
    if [[ -n $device ]]; then losetup -d "$device" || return 1; fi
    rm -rf "$dir"
}
trap cleanup EXIT
truncate -s 64M "$dir/disk"
if device=$(losetup --find --show "$dir/disk" 2>"$dir/error"); then
    mkfs.ext4 -q "$device"
    mkdir "$dir/mount"
    if mount "$device" "$dir/mount" 2>"$dir/error"; then
        echo preserved > "$dir/mount/proof"
        umount "$dir/mount"
        mount "$device" "$dir/mount"
        grep -qx preserved "$dir/mount/proof"
        echo "Native probe: this VM permits a loop-backed ext4 mount."
        exit 0
    fi
fi
cat "$dir/error" >&2
grep -Eiq 'permission denied|operation not permitted|cannot find an unused loop device|no such device' "$dir/error"
echo "Native probe: an unprivileged guest cannot complete the loop-backed ext4 exercise here."
BLOCK
cloud instances ssh "$instance" -- sudo touch "/var/tmp/$instance"
cloud instances stop "$instance"
cloud instances start "$instance"
cloud instances ssh "$instance" -- test -f "/var/tmp/$instance"
cloud instances delete "$instance"
created=false
labcompose down
started=false
remaining=$(labcompose ps -q)
[[ -z $remaining ]] || { echo "Compose containers remain: $remaining" >&2; exit 1; }
if podman container exists "mcl-vm-$instance" || podman volume exists "mcl-vm-$instance"; then
    echo "Test instance resources remain: $instance" >&2; exit 1
fi
echo "PASS: rootless instance create, SSH, persistence across stop/start and deletion"
if $full; then
    export LAB_IMAGE="localhost/mcl-test/lab:$revision"
    build lab "$LAB_IMAGE"
    python3 scripts/verify-lab.py --runtime podman --no-pull
fi
VERIFY
echo "PASS: $vm ($revision). Stop with: multipass stop $vm"
