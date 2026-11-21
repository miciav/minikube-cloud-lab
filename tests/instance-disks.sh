#!/usr/bin/env bash
# Run against a running rootful cloud built from this checkout.
set -euo pipefail
: "${TEST_CLOUD_CONTAINER:?Set TEST_CLOUD_CONTAINER to the running cloud container ID}"
cloud() { docker exec "$TEST_CLOUD_CONTAINER" cloud "$@"; }
id="disk-test-$(date +%s)-$$"
a="$id-a" b="$id-b"
created=()
cleanup() {
    for vm in "${created[@]}"; do cloud instances delete "$vm" || true; done
    if [[ ${disk_created:-} == yes ]]; then cloud disks delete "$id" || true; fi
}
trap cleanup EXIT
must_reject() {
    local expected=$1 output
    shift
    if output=$(cloud "$@" 2>&1); then echo "Unexpected success: $*" >&2; return 1; fi
    [[ "$output" == *"$expected"* ]] || { echo "$output" >&2; return 1; }
}
for vm in "$a" "$b"; do
    cloud instances create "$vm"
    created+=("$vm")
done
cloud disks create "$id" --size 64Mi
disk_created=yes
must_reject 'already exists' disks create "$id" --size 1Gi
cloud disks attach "$id" --instance "$a"
if [[ ${TEST_RESTART_CLOUD:-0} == 1 ]]; then
    docker restart "$TEST_CLOUD_CONTAINER" >/dev/null
    for _ in {1..20}; do if cloud disks list >/dev/null; then break; fi; sleep 1; done
    cloud disks list | grep -F "$a" >/dev/null
fi
must_reject 'attached' disks attach "$id" --instance "$b"
dev="/dev/cloud-disks/$id"
guest() { local vm=$1; shift; cloud instances ssh "$vm" -- "$@"; }
guest "$a" sudo mkfs.ext4 -q "$dev"
guest "$a" sudo mount "$dev" /mnt
guest "$a" sudo touch "/mnt/$id"
must_reject 'mounted' disks detach "$id" --instance "$a"
must_reject 'attached' disks delete "$id"
guest "$a" sudo umount /mnt
guest "$a" sudo systemd-run --unit=mcl-disk-reader /bin/sh -c "'exec 8<$dev; touch /run/mcl-reader-ready; exec sleep 60'"
for _ in {1..10}; do if guest "$a" test -e /run/mcl-reader-ready; then break; fi; sleep 1; done
guest "$a" test -e /run/mcl-reader-ready
must_reject 'busy' disks detach "$id" --instance "$a"
guest "$a" test -b "$dev"
guest "$a" sudo systemctl stop mcl-disk-reader
cloud instances stop "$a"
cloud instances start "$a"
guest "$a" sudo mount "$dev" /mnt
guest "$a" test -f "/mnt/$id"
guest "$a" sudo umount /mnt
cloud disks detach "$id" --instance "$a"
guest "$a" test ! -e "$dev"
cloud disks attach "$id" --instance "$b"
guest "$b" sudo mount "$dev" /mnt
guest "$b" test -f "/mnt/$id"
guest "$b" sudo umount /mnt
cloud instances delete "$b"
created=("$a")
cloud disks attach "$id" --instance "$a"
guest "$a" sudo mount "$dev" /mnt
guest "$a" test -f "/mnt/$id"
guest "$a" sudo umount /mnt
cloud disks detach "$id" --instance "$a"
cloud disks delete "$id"
disk_created=no
echo "PASS: independent disk lifecycle across two managed instances"
