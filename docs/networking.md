# Expose Nginx with NodePort and LoadBalancer

Start with a running lab cluster and its selected kubectl context. The exercise creates its own namespace and works with YAKD installed or removed.

Create a dedicated namespace and one Nginx replica for this exercise. **Predict:** can two LoadBalancer Services both use port 80? Which port
will you use from your computer?

```sh
kubectl create namespace service-demo
kubectl -n service-demo create deployment web --image=nginx:1.28-alpine --port=80
kubectl -n service-demo rollout status deployment/web
kubectl -n service-demo expose deployment web --name=web-node --type=NodePort --port=80 --target-port=80
kubectl -n service-demo expose deployment web --name=web-lb-one --type=LoadBalancer --port=80 --target-port=80
kubectl -n service-demo expose deployment web --name=web-lb-two --type=LoadBalancer --port=80 --target-port=80
kubectl -n service-demo get services
lab services
```

Wait a few seconds if LOCAL_ENDPOINT is empty, then repeat `lab services`. Copy
each displayed endpoint into `curl http://127.0.0.1:PORT` (replace PORT) and confirm
that all three return the Nginx welcome page. NodePort uses its Kubernetes NodePort on
localhost. Each LoadBalancer uses a different Docker IP and a free mapped host
port, even though both Services use port 80.

The LoadBalancer IP is reachable from the cluster network. Use the localhost endpoint from the lab host, including on macOS.

Unchanged Services keep their endpoints across a cloud restart. Changing ports
or type may interrupt connections and assign a new LoadBalancer host port.
Delete the exercise Services and check that their relay containers disappear:

```sh
kubectl delete namespace service-demo
lab services
```

Cluster deletion cleans up every owned relay.
The relay address pool needs the current /23 network; for a lab created with the
old /24 configuration, follow the [network migration instructions](operations.md#state-and-reset) before starting.

## Relay behavior

`lab services` reads the `minikube-cloud-lab.io/host-endpoints` JSON annotation
(`name`, `servicePort`, `nodePort`, `hostPort`) and
`minikube-cloud-lab.io/exposure-error`. Its Kubernetes equivalent is
`kubectl get nodes,services -A -o json`. The `NODE_ENDPOINTS` column combines
node InternalIPs with the Service's allocated NodePorts, including every node
and port; it requires permission to list Nodes. These are cluster-network
addresses, not connectivity checks. Use `LOCAL_ENDPOINT` from the Mac browser.
A missing local endpoint means publication is pending or failed; inspect the
ERROR column. A NodePort already in use on the computer
fails explicitly: free that port or change the Service's NodePort.

The first release supports TCP, including raw TLS and database connections,
with `externalTrafficPolicy: Cluster`. UDP/SCTP, `Local`, custom LoadBalancer
classes and `allocateLoadBalancerNodePorts: false` produce a visible error without
partial exposure. Changing type/ports can interrupt connections and change
LoadBalancer host ports; restarting the cloud preserves unchanged relays.
Each Service uses one small relay container from the cloud image. It has no
Docker socket or credentials and does not count as a cloud machine.
