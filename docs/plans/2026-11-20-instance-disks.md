# Standalone instance data disks on rootful Docker

Students create a blank named disk, attach it to one standalone instance, and use
standard Linux tools over SSH. The backing image belongs to the lab independently
of the instance's container and /var volume. Deleting the instance preserves the
disk. Formatting is always an explicit student operation.

## Proposed student workflow

    cloud disks create dati --size 1Gi
    cloud disks attach dati --instance vm1
    cloud instances ssh vm1
    sudo mkfs.ext4 /dev/cloud-disks/dati
    sudo mkdir -p /mnt/dati
    sudo mount /dev/cloud-disks/dati /mnt/dati
    echo hello | sudo tee /mnt/dati/message
    sudo umount /mnt/dati
    exit
    cloud disks detach dati --instance vm1
    cloud disks attach dati --instance vm2

On vm2 the student mounts the existing filesystem and reads message. The lab
must reject concurrent ownership, detachment while busy, and deletion while
attached. The guest path must resolve to this disk again after stop/start.
Loop numbers are kernel allocations and must never serve as disk IDs. Recovery
must verify the backing file before publishing or releasing a device.

## Runtime feasibility

Run `bash tests/instance-disk-feasibility.sh` on a trusted rootful Docker lab host.
It allocates a fresh Docker volume, creates a 64 MiB image and formats the loop
device in a disposable container based on the lab's Ubuntu image. It removes the
first container, then independently attaches the image in a second container
and checks the file contents. It deletes the test volume on exit. The check
requires loop support in the Docker host kernel and the guest image's
losetup, blockdev, mkfs.ext4 and mount commands.

This probe exercises the kernel, runtime and guest tools. Release acceptance also
requires running the student workflow against cloud-managed instances, checking
busy and duplicate attachment, and rebooting an attached instance and the cloud
service. Rootless Podman and Kubernetes PVCs are outside the first release.

On 2026-11-20 the probe passed on rootful Docker using the Ubuntu 26.04 instance
image at digest `sha256:0a5d61efc113b2ce3100318d474cc4406a42534ae9232c3d8e599797d6a29ef8`.
The second container read the marker written to ext4 by the first container.

## Implemented acceptance test

The rootful cloud exposes the commands above for standalone instances created
with disk support. `tests/instance-disks.sh` runs the exercise over the cloud
CLI and SSH, checks that a mounted disk cannot be detached or deleted, and
checks persistence across instance stop/start, transfer and deletion. With
`TEST_RESTART_CLOUD=1` it also restarts the cloud service and checks ownership.
The Student path workflow runs this test after the Kubernetes storage exercises.
