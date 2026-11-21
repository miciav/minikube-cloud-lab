# Autoscale a standalone HTTP service

Start the cloud service on a prepared Docker or rootless Podman host. Open two Bash or zsh terminals in the checkout and source `scripts/configure.sh` in each. This exercise needs room for three instances and uses the lab independently of Kubernetes.

## Create the group

Installing the module builds the Nomad Autoscaler target plugin locally and starts
Traefik, Prometheus and Nomad Autoscaler for all instance groups.

```bash
lab install instance-autoscaling
cloud groups create web --min 1 --desired 2 --max 3 --target-rps 10 \
  --bootstrap lab/instance-autoscaling/web.sh
cloud groups describe web
```

Wait until `cloud groups describe web` shows two ready members, then run `curl -fsS http://127.0.0.1:8084/groups/web/` to see which member answered.

## Observe scaling and replacement

Generate traffic in one terminal and inspect the
group and operation history in another. The policy can take several scrape
intervals to see changes in traffic.

```bash
while true; do curl -fsS http://127.0.0.1:8084/groups/web/ >/dev/null; done
```

```bash
cloud groups describe web
cloud instances list
cloud operations list
```

Identify the scale-out in the operation history. Stop the load with Ctrl+C and
watch the group return to its minimum after the cooldown. In Prometheus at
`http://127.0.0.1:9091`, query `traefik_service_server_up` for backend health
and `rate(traefik_service_requests_total[30s])` for traffic.

Send a request to get a member's name, then stop its application. The health
checks remove it from traffic. The group replaces the instance after the
failure persists through its grace period, bringing the service back at the
same desired capacity.

```bash
member=$(curl -fsS http://127.0.0.1:8084/groups/web/)
cloud instances ssh "$member" -- sudo systemctl stop lab-group-http
cloud groups describe web
cloud operations list
```

## How the group responds

Each group stores a versioned launch template with the instance image, machine
type and a bootstrap script. The script runs as root **inside each instance**
and must start the HTTP application. Its shebang selects the interpreter.
Scripts without a shebang run under `sh`. The default backend port is 8080 and the
default health path is `/health`. Traefik admits healthy members and routes
requests from `/groups/NAME/` to their application path.

Prometheus is available at `http://127.0.0.1:9091`. Nomad Autoscaler uses
requests per second per healthy backend to set the desired capacity within the
group limits. It waits for the current capacity to finish warming up and for a
cooldown between changes. Missing metric samples suspend automatic scaling.
The group controller also replaces a member that remains unhealthy. Use
`cloud operations list` to see capacity changes and instance operations.

## Clean up

**Uninstall deletes every group, its machines and their local filesystems.**
Traefik retires each registered backend before removal and allows up to
`--drain-seconds` for outstanding requests. Standalone machines created outside
groups and independently created persistent volumes are preserved. If cleanup
is interrupted, re-run the uninstall command. A new installation starts with
an empty inventory of groups.

```bash
lab uninstall instance-autoscaling
lab status instance-autoscaling
```

Expect `disabled`.
