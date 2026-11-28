# Ship your own image through a container registry

Start the cloud using the [course setup](../../../README.md#prepare-the-environment), create a cluster and select its context (`cloud clusters create demo` and `cloud clusters get-credentials demo`). Finish [Standalone Linux instances](instances-and-disks.md) first. The exercise needs room for one instance next to the cluster.

So far your clusters have run public images only. A *container registry* stores images you build yourself, the way ECR or GCR do in a public cloud. You build an image inside a VM, push it to a registry managed by the cloud and run it on Kubernetes.

Each registry has one endpoint, `NAME.registry:5000`, that VMs and cluster nodes reach by the same name, and two accounts:

| Account | Can | Give it to |
| --- | --- | --- |
| `push` | read and write | whoever builds images (here, the VM) |
| `pull` | read only | whoever only runs images (here, the cluster) |

The cluster gets the read-only account: if its Secret leaks, nobody can replace your images. This is least privilege, as with the two object storage keys.

## Create the registry

```sh
cloud registries create corso
cloud registries list
cloud registries credentials corso --push
cloud registries credentials corso --pull
```

`create` waits for the registry and prints its endpoint. `credentials` prints the endpoint, user name and password of one account. A lab has at most 3 registries; names follow the same rules as instance names.

## Build and push from a VM

Create a Debian instance, which ships Docker:

```sh
cloud instances create builder --image debian-12
```

The registry speaks plain HTTP (no TLS), so Docker inside the VM must be told to trust it. Print the lab subnet on your computer. It is `172.30.0.0/23` unless you changed `CLOUD_DOCKER_SUBNET` in `.env`:

```sh
docker network inspect minikube-cloud-lab --format '{{(index .IPAM.Config 0).Subnet}}'
# rootless Podman:
podman network inspect minikube-cloud-lab-rootless --format '{{range .Subnets}}{{.Subnet}}{{end}}'
```

Open a shell in the VM with `cloud instances ssh builder` and run the rest of this section there. Check `sudo cat /etc/docker/daemon.json` first: if the file exists, add the `insecure-registries` key to it instead of replacing it.

```sh
echo '{"insecure-registries": ["172.30.0.0/23"]}' | sudo tee /etc/docker/daemon.json
sudo systemctl restart docker
docker info --format '{{.RegistryConfig.InsecureRegistryCIDRs}}'
```

Build a two-line image and push it with the push account. Paste the push password when `docker login` asks for it:

```sh
mkdir hello && cd hello
printf 'FROM busybox:1.37\nCMD ["httpd", "-f", "-p", "8080"]\n' > Dockerfile
docker build -t corso.registry:5000/hello:v1 .
docker login -u push corso.registry:5000
docker push corso.registry:5000/hello:v1
```

**Predict:** what happens if you log in with the pull account and push a new tag?

```sh
docker login -u pull corso.registry:5000
docker tag corso.registry:5000/hello:v1 corso.registry:5000/hello:v2
docker push corso.registry:5000/hello:v2
exit
```

The login succeeds, because `pull` is a valid user, but the push is refused with `denied` or `unauthorized`. Back on your computer, the registry holds only `v1`:

```sh
cloud registries images corso
```

## Deploy from the registry

Nodes already trust lab registries; only the credentials are missing. Store the pull account in a Secret, replacing `PULL_PASSWORD`:

```sh
kubectl create secret docker-registry corso-pull --docker-server=corso.registry:5000 \
  --docker-username=pull --docker-password=PULL_PASSWORD
```

Save the Deployment as `hello.yaml`. `imagePullSecrets` tells the kubelet which Secret to use for this image:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
spec:
  replicas: 1
  selector:
    matchLabels: {app: hello}
  template:
    metadata:
      labels: {app: hello}
    spec:
      imagePullSecrets: [{name: corso-pull}]
      containers:
        - name: hello
          image: corso.registry:5000/hello:v1
          ports: [{containerPort: 8080}]
```

```sh
kubectl apply -f hello.yaml
kubectl rollout status deployment/hello
kubectl get pods -l app=hello
kubectl get events --field-selector reason=Pulled
```

The events show the kubelet pulling `corso.registry:5000/hello:v1`. The pull succeeds only because the Secret gives the kubelet the pull account: the registry refuses anonymous clients.

## A registry lives with the cloud

Stopping the cloud stops its registries; starting it brings them back on their data. While the cloud is down, pulls fail and the kubelet retries. `labcompose down -v` erases all registry data. On rootful Docker the registries come back empty with the same names and credentials; on rootless Podman it also erases the cloud state, so the registries are gone. See [operations](../../operations.md#container-registries).

## Clean up

```sh
kubectl delete deployment hello
kubectl delete secret corso-pull
cloud instances delete builder
cloud registries delete corso          # refused: the registry still holds images
cloud registries delete corso --force
```

A registry that still holds images is deleted only with `--force`, like a non-empty repository in ECR.

## Limits

- HTTP only: anyone on the lab network can read the traffic. Use the registry for course images, never for secrets.
- Clusters created before registries existed do not trust the lab subnet: delete and recreate them. Clusters on rootless Podman pull through a per-registry containerd entry that the cloud writes on every node; that path is not covered by CI.
- On rootful Docker, registry data uses volume subpaths, which need Docker Engine 26 (API 1.45) or later, like instance data disks.
- Registries are not yet billed or shown in the portal.
