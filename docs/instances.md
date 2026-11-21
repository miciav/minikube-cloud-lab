# Standalone Linux instances

Start the cloud using the [course setup](../README.md#prepare-the-environment). These exercises can run before creating a Kubernetes cluster.

Besides the cluster, the cloud runs standalone Linux instances: system containers
with systemd, SSH, `sudo` and `apt`, on which students can install packages and
run services, including their own Docker Engine. They use the same machine types
and asynchronous operations as cluster machines.

```sh
cloud images list
cloud instances create vm1 --machine-type lab-small
cloud instances ssh vm1
cloud instances list
cloud instances stop vm1
cloud instances start vm1
cloud instances delete vm1
```

| Image | System | SSH user | Docker |
| --- | --- | --- | --- |
| `ubuntu-26.04` (default) | Ubuntu 26.04 LTS with the tools tutorials expect (curl, wget, nano, git, ping, ip) | `ubuntu` | Not installed: follow [Docker's Ubuntu guide](https://docs.docker.com/engine/install/ubuntu/) |
| `debian-12` | Debian 12, the minikube node image (kicbase) without Kubernetes | `docker` | Preinstalled: `sudo docker run --rm hello-world` |

Both users have passwordless `sudo`, and services installed with `apt` start as on
a regular machine; SSH logins get a systemd user session (`systemctl --user`) on
Ubuntu. Choose an image with `--image debian-12`. The Ubuntu image is published with
the cloud images and downloaded on the first create, which therefore takes longer
(`docker compose logs cloud` shows the download); `UBUNTU_IMAGE` in `.env` selects
another build. Compose does not pull it: after updating the lab, refresh it with
`docker pull ghcr.io/miciav/minikube-cloud-lab/instance-ubuntu:26.04` (`podman pull`
on Podman). Existing instances keep the image they were created from. Each instance gets its own SSH key, stored in the cloud data
directory and never shown in `cloud instances describe`; `cloud instances ssh`
connects from the cloud container and verifies the instance host key. Use
`--` before a remote command; its exit status becomes the command's exit status.

At most 8 instances exist at a time, counting stopped and failed ones until they are
deleted; they use addresses .200–.252 of the lab network.
Instances reach each other, the cluster nodes and the Internet. `/var` (including
Docker images and containers) lives in a dedicated volume and the rest of the
filesystem in the container: both survive stop/start and cloud restarts and are
removed by `cloud instances delete`. Delete instances before `docker compose down`,
as with clusters: they stay attached to the lab network.

Instances are privileged containers, like the cluster nodes: on rootful Docker,
root inside an instance is root on the Docker host; on Podman rootless it is the
student's own user. The SSH private key is served by the management API, which has
no authentication and listens only on localhost, like cluster kubeconfigs. Ubuntu
instances generate their own SSH host keys at first boot; `debian-12` instances share
the host keys baked into kicbase. Each connection is checked against the key recorded
for that instance. Use instances only on a trusted lab host.

## Configure Ubuntu or Debian at first boot

With the cloud and instance images built from the same revision, create an instance
using a local YAML file (up to 64 KiB):

```sh
cloud instances create app --user-data app.yaml
```

For example, `app.yaml` can install and start a web server:

```yaml
#cloud-config
users:
  - default
  - name: deploy
    ssh_authorized_keys:
      - ssh-ed25519 REPLACE_WITH_YOUR_PUBLIC_KEY
package_update: true
packages: [nginx]
runcmd:
  - [systemctl, enable, --now, nginx]
```

Replace the placeholder with your public key. Standard cloud-init modules also
support password hashes, SSH password authentication, package upgrades and file
creation. For Debian with Docker Engine, run:

```sh
cloud instances create app --image debian-12 --user-data app.yaml
```

With user-data, Debian uses a kicbase image with cloud-init installed. Set
`DEBIAN_IMAGE` to select a local build. The lab retains SSH access as `ubuntu`
on Ubuntu and `docker` on Debian. Creation waits for cloud-init to finish. Stop/start preserves its state; deleting and recreating the instance
applies the supplied configuration again. To stream YAML use `--user-data -`.
Run the lifecycle test against a running lab with
`TEST_CLOUD_CONTAINER=$(labcompose ps -q cloud) RUNTIME=$CLOUD_LAB_RUNTIME bash tests/instance-cloud-init.sh debian-12`.
Omit the image argument to test Ubuntu.

## Persistent disks for standalone instances

With rootless Podman, use the [QEMU disk console exercise](podman-rootless.md#student-disk-exercise-with-qemu).

On rootful Docker, a student can create a blank disk and attach it to a running
instance. Use instances created with this release. Each disk is a sparse file
in the Docker volume `minikube-cloud-lab-disks`. Its capacity is independent of
the Kubernetes PVC budget. A write consumes real space on the Docker host.

With two running instances named `vm1` and `vm2`, run:

```sh
cloud disks create dati --size 1Gi
cloud disks attach dati --instance vm1
cloud instances ssh vm1 -- sudo mkfs.ext4 /dev/cloud-disks/dati
cloud instances ssh vm1 -- sudo mount /dev/cloud-disks/dati /mnt
cloud instances ssh vm1 -- sudo touch /mnt/created-on-vm1
cloud instances ssh vm1 -- sudo umount /mnt
cloud disks detach dati --instance vm1
cloud disks attach dati --instance vm2
cloud instances ssh vm2 -- sudo mount /dev/cloud-disks/dati /mnt
cloud instances ssh vm2 -- ls -l /mnt/created-on-vm1
```

Format a new disk once. Attaching it again preserves its filesystem. The
device path returns after an instance restart. Mount the filesystem again
to access its contents. `cloud disks list` shows disk sizes and owners.
Unmount the filesystem before detaching. Each disk can belong to one instance
at a time. Deleting the instance releases the disk for reuse. To erase the
disk itself, detach it and run `cloud disks delete dati`. Docker Compose's
`down -v` also deletes the named disk volume and all its images.
