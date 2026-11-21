#!/usr/bin/env bash
# Provision a dedicated Multipass VM and verify the committed checkout as a rootless user.
set -euo pipefail
vm=mcl-rootless-test
full=false
ephemeral=false
created_vm=false
usage() { echo "Usage: $0 [--vm NAME] [--full] [--ephemeral]"; }
while (( $# )); do
    case $1 in
        --vm) vm=${2:?missing VM name}; shift 2 ;;
        --ephemeral) ephemeral=true; shift ;;
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
cleanup_host() {
    local rc=$?
    rm -rf "$scratch"
    if $ephemeral && $created_vm; then multipass delete --purge "$vm" || exit 1; fi
    exit "$rc"
}
trap cleanup_host EXIT
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
CLOUD
    if $ephemeral; then
        ssh-keygen -q -t ed25519 -N '' -f "$scratch/key"
        printf 'ssh_authorized_keys:\n  - ' >> "$scratch/cloud-init.yaml"
        cat "$scratch/key.pub" >> "$scratch/cloud-init.yaml"
    fi
    multipass launch 26.04 --name "$vm" --cpus 4 --memory 8G --disk 48G --cloud-init "$scratch/cloud-init.yaml"
    created_vm=true
else
    if $ephemeral; then echo "Temporary VM name already in use" >&2; exit 1; fi
    multipass start "$vm"
fi
guest() {
    if ! $ephemeral; then multipass exec "$vm" -- "$@"; return; fi
    local ip command
    ip=$(multipass info "$vm" --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["info"][sys.argv[1]]["ipv4"][0])' "$vm") || return
    printf -v command '%q ' "$@"
    ssh -i "$scratch/key" -o BatchMode=yes -o ConnectTimeout=10 -o IdentitiesOnly=yes \
        -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$scratch/known_hosts" "ubuntu@$ip" "$command"
}
# Give commands such as compose exec their own stdin, separate from the script.
guest_script() {
    guest bash -c 'file=$(mktemp) || exit; cat > "$file" && bash -e "$file" "$@" </dev/null; rc=$?; rm -f "$file"; exit "$rc"' verify "$@"
}
guest_script <<'PROVISION'
[[ -f /etc/mcl-multipass-rootless-test ]] || { echo "Refusing to modify an unrelated VM" >&2; exit 1; }
if ! command -v podman >/dev/null || ! command -v podman-compose >/dev/null; then
    sudo apt-get update
    sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y podman podman-compose uidmap dbus-user-session fuse-overlayfs slirp4netns
fi
# Ubuntu's pasta AppArmor profile denies the termination signal from Podman at teardown.
mkdir -p "$HOME/.config/containers/containers.conf.d"
printf '%s\n' '[network]' 'default_rootless_network_cmd="slirp4netns"' > "$HOME/.config/containers/containers.conf.d/90-mcl-test.conf"
# The Docker daemon inside Debian instances uses legacy iptables and cannot load host modules.
modules=(br_netfilter vxlan iptable_nat iptable_filter iptable_mangle iptable_raw)
printf '%s\n' "${modules[@]}" | sudo tee /etc/modules-load.d/cloud-lab.conf >/dev/null
sudo modprobe -a "${modules[@]}"
sudo loginctl enable-linger "$(id -un)"
systemctl --user enable --now podman.socket
systemctl --user enable podman-restart.service
test "$(id -u)" != 0
podman info --format 'Podman {{.Version.Version}} rootless={{.Host.Security.Rootless}} cgroups={{.Host.CgroupsVersion}}'
test "$(podman info --format '{{.Host.Security.Rootless}}')" = true
PROVISION
git -C "$root" archive --format=tar.gz -o "$scratch/source.tgz" "$revision"
guest_archive=$(guest mktemp /tmp/mcl-source-XXXXXXXX.tgz)
guest bash -c 'cat > "$1"' archive "$guest_archive" < "$scratch/source.tgz"
guest_script "$revision" "$full" "$guest_archive" <<'VERIFY'
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
export UBUNTU_IMAGE="localhost/mcl-test/instance-ubuntu:$revision"
export DEBIAN_IMAGE="localhost/mcl-test/instance-debian:$revision"
build instance-debian "$DEBIAN_IMAGE"
build instance-ubuntu "$UBUNTU_IMAGE"
build cloud "$CLOUD_IMAGE"
source scripts/configure.sh

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
cloud_id=$(podman ps -q --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME" --filter label=com.docker.compose.service=cloud)
[[ -n $cloud_id && $cloud_id != *$'\n'* ]]
for image in ubuntu-26.04 debian-12; do
    TEST_CLOUD_CONTAINER=$cloud_id RUNTIME=podman bash tests/instance-cloud-init.sh "$image"
done
python3 tests/rootless-disk-console.py
if $full; then bash tests/instance-autoscaling.sh; fi
cloud instances create "$instance"
created=true
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
if $full && $ephemeral; then
    guest_script "$revision" <<'BEFORE_REBOOT'
cd "$HOME/mcl-test/$1"
export CLOUD_RUNTIME=podman COMPOSE_PROJECT_NAME=mcl-rootless-verification
export CLOUD_IMAGE="localhost/mcl-test/cloud:$1"
source scripts/configure.sh
labcompose up -d cloud
lab install instance-autoscaling
BEFORE_REBOOT
    boot_id=$(guest cat /proc/sys/kernel/random/boot_id)
    guest sudo systemctl reboot || true # SSH may close before systemctl returns.
    for _ in {1..60}; do
        next_boot=$(guest cat /proc/sys/kernel/random/boot_id 2>/dev/null) || { sleep 2; continue; }
        if [[ $next_boot != "$boot_id" ]]; then break; fi
        sleep 2
    done
    [[ -n ${next_boot:-} && $next_boot != "$boot_id" ]]
    guest_script "$revision" <<'AFTER_REBOOT'
cd "$HOME/mcl-test/$1"
export CLOUD_RUNTIME=podman COMPOSE_PROJECT_NAME=mcl-rootless-verification
source scripts/configure.sh
for _ in {1..60}; do
    if curl -fsS http://127.0.0.1:8080/v1/instance-autoscaling/health |
        python3 -c 'import json,sys; sys.exit(json.load(sys.stdin)["state"] != "ready")'; then break; fi
    sleep 2
done
curl -fsS http://127.0.0.1:8080/v1/instance-autoscaling/health |
    python3 -c 'import json,sys; assert json.load(sys.stdin)["state"] == "ready"'
lab uninstall instance-autoscaling
labcompose down
AFTER_REBOOT
fi
if $ephemeral; then
    echo "PASS: $vm ($revision); removing test VM"
else
    echo "PASS: $vm ($revision). Stop with: multipass stop $vm"
fi
