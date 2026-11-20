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
python3 tests/rootless-disk-console.py
created=true
cloud instances create "$instance"
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
