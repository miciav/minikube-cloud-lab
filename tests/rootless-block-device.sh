#!/usr/bin/env bash
# Run inside the disposable Multipass test VM. sudo simulates the host-side
# allocation of a single device. Both students' containers run rootless.
set -euo pipefail
[[ -f /etc/mcl-multipass-rootless-test && $(id -u) != 0 ]]
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]]
image=${UBUNTU_IMAGE:-ghcr.io/miciav/minikube-cloud-lab/instance-ubuntu:26.04}
backing=$(mktemp "$HOME/mcl-device-XXXXXXXX")
node=
loop=
cleanup() {
    if [[ -n $node ]]; then sudo rm -f "$node/data" && sudo rmdir "$node" || return 1; fi
    if [[ -n $loop ]]; then sudo losetup -d "$loop" || return 1; fi
    rm -f "$backing"
}
trap cleanup EXIT
truncate -s 64M "$backing"
node=$(sudo mktemp -d /dev/mcl-disk-XXXXXXXX)
loop=$(sudo losetup --find --show "$backing")
read -r major minor < <(stat -c '%t %T' "$loop")
# Own a private device node. Leave the system's reusable /dev/loopN permissions intact.
sudo mknod -m 600 "$node/data" b "$((16#$major))" "$((16#$minor))"
sudo chown "$(id -u):$(id -g)" "$node/data"
sudo chmod 711 "$node"

for phase in format read; do
    podman run --rm -i --privileged --device "$node/data:/dev/cloud-disks/dati" \
        --device /dev/fuse --entrypoint bash "$image" -se -- "$phase" <<'GUEST'
dev=/dev/cloud-disks/dati
test -b "$dev"
test "$(blockdev --getsize64 "$dev")" = 67108864
if ! command -v fuse2fs >/dev/null; then
    apt-get -qq update
    apt-get -qq install -y --no-install-recommends fuse2fs >/dev/null
fi
if [[ $1 == format ]]; then mkfs.ext4 -q "$dev"; fi
mkdir /tmp/disk-mount
fuse2fs "$dev" /tmp/disk-mount
mountpoint -q /tmp/disk-mount
if [[ $1 == format ]]; then
    echo 'written by the first guest' > /tmp/disk-mount/proof
else
    grep -qx 'written by the first guest' /tmp/disk-mount/proof
fi
umount /tmp/disk-mount
GUEST
done
echo 'PASS: two rootless guests shared a host-provisioned block device in succession.'
