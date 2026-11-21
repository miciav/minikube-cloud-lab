#!/usr/bin/env bash
# Exercise a blank file-backed block device in two disposable guest containers.
set -euo pipefail

image=${UBUNTU_IMAGE:-ghcr.io/miciav/minikube-cloud-lab/instance-ubuntu:26.04}
engine=$(docker info --format '{{.OSType}} {{json .SecurityOptions}}')
if [[ $engine != linux* || $engine == *rootless* || $engine == *userns* ]]; then
    echo "This probe requires rootful Linux Docker without user namespace remapping." >&2
    exit 1
fi

# Let the daemon allocate a fresh volume. Never use an existing student's disk.
volume=$(docker volume create)
cleanup() { docker volume rm "$volume" >/dev/null; }
trap cleanup EXIT
for phase in write read; do
    docker run --rm -i --privileged --network none --mount "type=volume,src=$volume,dst=/disk" \
        --entrypoint bash "$image" -seu -- "$phase" <<'GUEST'
phase=$1
image=/disk/dati.img
if [[ $phase == write ]]; then
    (set -o noclobber; : > "$image")
    truncate -s 64M "$image"
fi
test -f "$image"
test -z "$(losetup -j "$image")"
device=$(losetup --find --show "$image")
mountpoint=/mnt/data
cleanup() {
    if mountpoint -q "$mountpoint"; then umount "$mountpoint" || return 1; fi
    losetup -d "$device"
}
trap cleanup EXIT
mkdir -p /dev/cloud-disks "$mountpoint"
ln -s "$device" /dev/cloud-disks/dati
test -b /dev/cloud-disks/dati
test "$(blockdev --getsize64 /dev/cloud-disks/dati)" = 67108864
if [[ $phase == write ]]; then
    mkfs.ext4 -q /dev/cloud-disks/dati
fi
mount /dev/cloud-disks/dati "$mountpoint"
if [[ $phase == write ]]; then
    echo 'disk survived guest deletion' > "$mountpoint/student.txt"
else
    test "$(cat "$mountpoint/student.txt")" = 'disk survived guest deletion'
fi
GUEST
done
echo 'PASS: a second guest read the ext4 filesystem created by the first.'
