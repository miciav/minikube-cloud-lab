#!/bin/sh
set -eu
name=$1
image=/var/lib/mcl-disk-images/$name
dir=/dev/cloud-disks
mkdir -p "$dir"
# Hold an open descriptor so the kernel releases the mapping on guest shutdown.
device=$(losetup --find --show --nooverlap "$image")
exec 9<>"$device"
losetup -d "$device"
ln -sfn "$device" "$dir/$name"
systemd-notify --ready
exec sleep infinity
