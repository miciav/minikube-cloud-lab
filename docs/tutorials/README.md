# Tutorials by topic

Start with the [course environment](../../README.md#prepare-the-environment). Each guide gives its required starting state and cleanup steps. For a class following the slide decks, use the original sequence and terminal instructions in [Lecture 02](../../kubernetes/lecture02/README.md) or [Lecture 03](../../kubernetes/lecture03/README.md).

## Compute

- [Standalone Linux instances and data disks](compute/instances-and-disks.md) — bootstrap an instance, connect to it and move its data disk to a second instance. Start with a running cloud.
- [Golden images](compute/golden-images.md) — configure one Ubuntu instance, capture it as a personal image, launch copies that need no installation and compare with cloud-init. Start with a running cloud.
- [Container registry](compute/container-registry.md) — build an image in a Debian instance, push it to a lab registry and deploy it on your cluster with a read-only pull Secret. Start with a running cluster.

## Costs

- [Check usage and costs](billing/costs.md) — read `cloud billing` after creating a cluster, scaling it and stopping an instance. Start with a running cloud.

## Kubernetes workloads and scheduling

- [Deployments, placement, rollouts and drain](../../kubernetes/lecture02/README.md#exercise-files) — use exercises 1–5 for replicas and placement, and 8–12 for updates, DaemonSets and node maintenance. Start with one node and add two workers before exercise 5.
- [Multi-service applications](../../kubernetes/lecture02/README.md#application-details) — run Adminer with MariaDB or trace a vote from the frontend through Redis and PostgreSQL to the result service. Follow exercises 15 and 16 in the [command transcript](../../kubernetes/lecture02/commands.txt).

## Autoscaling

| What changes | Exercise | What to observe |
| --- | --- | --- |
| Pod replicas | [Horizontal Pod Autoscaler](../../kubernetes/lecture03/README.md#hpa) | HTTP load increases CPU utilization and the Deployment gains replicas. |
| Pod resource requests | [Vertical Pod Autoscaler](../../kubernetes/lecture03/vpa/README.md) | Compare recommendations with the requests on replacement Pods. |
| Kubernetes nodes | [From Pending Pod to cloud machine](autoscaling/kubernetes-nodes.md) | Match a scheduling event to the cloud operation that adds a worker. The [manifest-based exercise](../../kubernetes/lecture03/CLUSTER_AUTOSCALING.md) follows the slide deck. |
| Pod replicas on a schedule | [KEDA Cron scaler](../../kubernetes/lecture03/keda/README.md) | Watch the workload scale from zero to two and back to zero across a UTC time window. |
| Standalone HTTP instances | [Instance groups](autoscaling/instance-groups.md) | Observe traffic-driven scaling, backend health and instance replacement. A running cloud is sufficient. |

## Networking

- [Expose Nginx through Kubernetes Services](networking/service-exposure.md) — reach one Nginx Deployment through a NodePort and two LoadBalancer Services that share port 80.
- [Cross-worker networking and service discovery](../../kubernetes/lecture02/commands.txt) — assign exercises 6 and 7 after adding the two workers. Trace traffic between Redis Pods and between a client and Nginx.

## Persistent storage

- [Persistent volumes and recovery](storage/persistent-volumes.md) — trace a claim to its backing volume and recover retained data after replacing a dedicated cluster.
- [MongoDB replica set](../../kubernetes/lecture02/MONGODB.txt) — inspect the persistent claims, initialize replication once and verify one primary and two secondaries.

## Custom resources and operators

- [Define a CronTab resource](../../kubernetes/lecture03/README.md#crd) — register a new resource type and inspect an instance through the Kubernetes API.
- [Run MySQL with an operator](../../kubernetes/lecture03/README.md#mysql-operator) — verify three online members, connect through Router and follow cleanup through volume release.

## Managed services

- [Object storage](storage/object-storage.md) — upload, list and download objects with two access levels.
- [Messaging](messaging/queues.md) — observe delivery, visibility timeout and acknowledgment.
- [Database](databases/postgresql.md) — persist relational data and reconnect after reinstall.

For the combined exercise install all three modules and run `bash lab/services/examples/csv-worker.sh` from the checkout. Inspect the reported job in the course-files bucket and the csv_results table. The worker processes the same message twice after a simulated interruption. A unique job key keeps a single committed result.

## Infrastructure as code

[Deploy a CSV application across a VM and Kubernetes](../../lab/capstone/README.md). Use OpenTofu and cloud-init to provision the VM and cluster, Ansible to configure the VM, and kubectl to deploy the supplied worker.
