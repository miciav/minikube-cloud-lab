#!/usr/bin/env bash
set -euo pipefail
[[ $# == 2 ]] || { echo "Usage: $0 INSTANCE DISK" >&2; exit 2; }
for name in "$@"; do
    [[ $name =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || { echo 'Invalid instance or disk name' >&2; exit 2; }
done
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || { echo 'Rootless Podman required' >&2; exit 1; }
volume=$(podman volume inspect --format '{{.Mountpoint}}' minikube-cloud-lab-rootless-disks)
slot=$volume/slots/$1
[[ -f $slot/$2 ]] || { echo "Disk $2 is not attached to $1" >&2; exit 1; }
context=$(cd "$(dirname "$0")/../lab/disk-console" && pwd)
image=localhost/minikube-cloud-lab/disk-console:$(cat "$context"/{Containerfile,init,run} | sha256sum | cut -c1-16)
if ! podman image exists "$image"; then
    podman build -t "$image" "$context" >&2
fi
tty=()
if [[ -t 0 && -t 1 ]]; then tty=(-t); fi
exec podman run --rm -i "${tty[@]}" --pid="container:mcl-vm-$1" --network none --cap-drop all \
    --security-opt no-new-privileges --mount "type=bind,src=$slot,dst=/slot" "$image" "$2"
