# Golden images

Start the cloud using the [course setup](../../../README.md#prepare-the-environment). Finish [Standalone Linux instances](instances-and-disks.md) first. This exercise needs room for five instances and uses the lab independently of Kubernetes. It also installs the optional instance autoscaling module for the last step.

A *golden image* is a machine you configure once and then copy. You build one instance by hand, stop it, capture it as a personal image and launch any number of copies that already contain the work. Compare it with the two ways you have seen so far to configure a machine:

| Approach | When the work happens | Typical use |
| --- | --- | --- |
| cloud-init (`--user-data`) or a group bootstrap script | On every launch, while you wait | Settings that differ per copy; short setups |
| Golden image | Once, before capture | Slow, identical work: packages, downloaded Docker images, application files |

## Prepare the source instance

Create an Ubuntu instance and turn it into the machine you want to copy. The commands install nginx and Docker Engine, publish a page, create a user and download a Docker image:

```sh
cloud instances create web-src
cloud instances ssh web-src -- sudo apt-get update -qq
cloud instances ssh web-src -- sudo apt-get install -y -qq nginx docker.io
cloud instances ssh web-src -- 'echo "<h1>Golden image works</h1>" | sudo tee /var/www/html/index.html'
cloud instances ssh web-src -- sudo useradd -m -s /bin/bash mario
cloud instances ssh web-src -- sudo docker pull nginx:alpine
cloud instances ssh web-src -- curl -s localhost
```

The last command prints the page. Everything above lives in the instance's root filesystem and in `/var`, where `apt` and Docker keep their data.

## Capture the image

The source must be stopped, so that the files are consistent, and must be a standalone instance with no attached data disk:

```sh
cloud instances stop web-src
cloud images create web-golden --from web-src --description "Ubuntu with nginx, Docker and mario"
cloud images list
cloud images describe web-golden
```

`cloud images list` shows the source, the built-in image it derives from (`BASE`), its status and its size in bytes. While the capture runs, add `--async` to get the operation ID back immediately; the source cannot be started or deleted until the image is `AVAILABLE`. You can delete `web-src` afterwards, or keep it to change the image later: capture again under a new name.

## Launch copies

A personal image is used like a built-in one:

```sh
cloud instances create web1 --image web-golden
cloud instances create web2 --image web-golden
cloud instances ssh web1 -- curl -s localhost
cloud instances ssh web2 -- curl -s localhost
cloud instances ssh web1 -- id mario
cloud instances ssh web1 -- sudo docker images
cloud instances ssh web1 -- 'sudo docker run --pull=never --rm nginx:alpine nginx -v 2>&1 | tail -1'
```

Both copies serve the page as soon as `create` returns: nothing was installed at launch. Docker finds `nginx:alpine` locally, so `--pull=never` succeeds without a download. The image holds the root filesystem and a copy of `/var`, including the inner Docker images and containers.

What stays unique to each copy? Compare the copies:

```sh
for vm in web1 web2; do
  cloud instances ssh $vm -- hostname
  cloud instances ssh $vm -- cat /etc/machine-id
  cloud instances ssh $vm -- sudo cat /etc/ssh/ssh_host_ed25519_key.pub
done
cloud instances list
```

Each copy has its own hostname, `machine-id`, SSH host keys, cloud SSH key and IP address, so copies never impersonate each other or the source. SSH keys that you added to `authorized_keys` yourself are kept; the cloud key of the source is removed.

## Compare with cloud-init

Save the same setup as `golden.yaml`:

```yaml
#cloud-config
users:
  - default
  - name: mario
    shell: /bin/bash
package_update: true
packages: [nginx, docker.io]
write_files:
  - path: /var/www/html/index.html
    content: |
      <h1>Golden image works</h1>
runcmd:
  - [systemctl, enable, --now, docker]
  - [docker, pull, "nginx:alpine"]
```

Time one launch of each kind:

```sh
time cloud instances create web3 --image web-golden
time cloud instances create web-ci --user-data golden.yaml
```

Both instances end up with the same page, user and Docker image, but `create` returns much sooner for the copy of the golden image: the packages and the download happened once, at capture time. With cloud-init the installation and the download repeat for every instance and depend on your network. Large `/var` directories work the other way: the more an image holds, the longer each launch takes to copy it.

## Use the image in a group

A group launched from a personal image needs no bootstrap script because the application is already installed and starts at boot (`nginx` is enabled by `apt`). `--bootstrap` remains available and is still required for built-in images. Delete the comparison instances, install the module as in [Autoscale an HTTP service](../autoscaling/instance-groups.md) and create the group. This image serves on port 80, so override the group defaults:

```sh
cloud instances delete web3
cloud instances delete web-ci
lab install instance-autoscaling
cloud groups create web --image web-golden --port 80 --health-path / --min 1 --desired 2 --max 3 --target-rps 10
cloud groups describe web
curl -fsS http://127.0.0.1:8084/groups/web/
```

Repeat `cloud groups describe web` until it shows two ready members. OpenTofu configurations use the image as well, with `image = "web-golden"` in a `lab_instance` resource: nothing else changes.

## What goes in the image and what in cloud-init

Put what is identical on every copy in the image: packages, application code, downloaded Docker images, base configuration. Pass what must differ per copy at launch with `--user-data`, which works on a personal image whose source had cloud-init (Ubuntu, or Debian created with user-data). For example, with `copy.yaml`:

```yaml
#cloud-config
runcmd:
  - [sh, -c, 'echo "<h1>Served by $(hostname)</h1>" > /var/www/html/index.html']
```

```sh
cloud instances create web4 --image web-golden --user-data copy.yaml
cloud instances ssh web4 -- curl -s localhost
```

Hostnames written into configuration files, per-instance users and secrets belong here. If the source was a Debian instance created without user-data, the cloud rejects `--user-data` for its images.

Other facts to keep in mind:

- The login user comes from the base image (`ubuntu` or `docker`), because the cloud still connects as that user. Users you created, like `mario`, exist in every copy.
- Detach data disks before capture: the image never contains a disk, and a copy has none. A mount line you wrote by hand, for example in `/etc/fstab`, travels with the image and points to a device the copy does not have.
- The capture is a point in time. Later changes to the source are not in the image.

## Clean up

Delete everything in this order: the group, the instances, then the image. An image in use by an instance or group cannot be deleted.

```sh
cloud groups delete web
cloud groups list
cloud instances delete web4
cloud instances delete web2
cloud instances delete web1
cloud instances delete web-src
cloud images delete web-golden
cloud images list
```

Wait until `cloud groups list` no longer shows `web` before deleting the image. Run `lab uninstall instance-autoscaling` if you installed the module only for this exercise.

## Costs and limits

Each image stores its own copy of `/var` and each copy gets another one, so large images use host disk space and make launches slower. At most 8 personal images exist at a time. They are visible only in your own cloud.
Personal images live in the local container runtime, not only in the cloud's state: `docker image prune -a` or `docker volume prune` (`podman` on Podman) can remove them. The cloud then shows the image as `ERROR` after the next start. Delete it with `cloud images delete NAME` and capture it again.
