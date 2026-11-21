# Deploy a CSV application across a VM and Kubernetes

Start with a dedicated running cloud and no managed cluster. Use the lab and Ubuntu images from the same course version; the instance image must include cloud-init. Allow capacity for the API instance and one lab-small Kubernetes node plus the course services. Both Docker and rootless Podman use the same shell commands.

| Component | Owner | Work performed |
| --- | --- | --- |
| VM and cluster | OpenTofu, local `course/lab` provider | Create and read infrastructure through the cloud API, wait for operations and remove resources on destroy. The lab manages one cluster at a time. |
| First VM boot | cloud-init | Create the deployer account, enroll its public key and write the machine role marker. Changing the first-boot configuration replaces the VM. |
| API and dependencies | Ansible | Install packaged Python libraries and a systemd service. The API writes the CSV to Garage, queues its ID, serves leased jobs and commits a worker result in PostgreSQL before acknowledging the message. |
| Worker | kubectl | Deploy the supplied Python worker. It requests jobs from the API, sums the quantity column and reports the result. It can restart between jobs. |

The containerized tools use the current directory as `/workspace`. Run all exercise commands from `lab/capstone` after sourcing the helper. The lab image includes OpenTofu, the provider binary, Ansible, Python and OpenSSH. No host Go toolchain, provider registry download or native Ansible installation is required.

## Create the infrastructure

From the project root in a Bash or zsh terminal:

```sh
source scripts/configure.sh
labcompose up -d cloud
lab install object-storage
lab install queue
lab install database
cd lab/capstone
mkdir -p -m 700 .local
labtool ssh-keygen -q -t ed25519 -N '' -f .local/deploy
tofu -chdir=infrastructure init
tofu -chdir=infrastructure plan
tofu -chdir=infrastructure apply -parallelism=1
tofu -chdir=infrastructure plan
```

Generate the key once per checkout. The first apply sends a `#cloud-config` document as the VM's `user_data`; the private key stays in `.local/deploy`. The provider sends only the public key in this example. OpenTofu records input values in state, including user data marked sensitive, so keep passwords and private keys out of cloud-init. Keep the local directory and state private. The second plan should show no changes. This introductory provider replaces the resource when its declared inputs change, including cluster size. Review the plan before applying any change to a cluster containing workloads.

## Configure the API

```sh
bash prepare.sh
ansible-playbook -i .local/inventory.yml --private-key .local/deploy ansible/site.yml
ansible-playbook -i .local/inventory.yml --private-key .local/deploy ansible/site.yml
```

Preparation joins the API VM to the three installed services' private networks. It reads the lab's recorded SSH host key and writes an inventory and private application settings into `.local`. Ansible connects over SSH from the lab network as `deployer`. The playbook verifies the HTTP health endpoint. Expect `changed=0` on the second run.

## Deploy and exercise the worker

```sh
cloud clusters get-credentials csv-workers
kubectl create namespace csv-capstone
kubectl -n csv-capstone create secret generic csv-worker-access --from-env-file=.local/worker.env
kubectl -n csv-capstone apply -k kubernetes
kubectl -n csv-capstone rollout status deployment/csv-worker
ip=$(tofu -chdir=infrastructure output -raw api_address)
printf 'quantity\n3\n5\n' | cloud instances ssh csv-api -- curl -fsS --data-binary @- "http://$ip:8090/jobs"
```

Copy the returned job ID, wait for the worker, then run:

```sh
cloud instances ssh csv-api -- curl -fsS "http://$ip:8090/results/JOB_ID"
kubectl -n csv-capstone logs deployment/csv-worker
```

Expect `{"total": 8}`. A result request returns 404 until the worker completes the job. For a second experiment scale the worker to zero, submit another CSV, then restore one replica and watch the waiting job finish. The submission and result endpoints are for the trusted course network. The worker token grants access to the lease and completion endpoints. After replacing the VM rerun preparation and the playbook, update the Secret from the new worker.env, and restart the Deployment.

## Observe the backlog

Install `monitoring` before submitting jobs, then open the `portal` module and select **Monitoring**. Use `kubectl -n csv-capstone scale deployment/csv-worker --replicas=0` and wait for the worker pod to terminate. Submit ten CSVs using the job submission command above. The bucket should gain ten objects and the queue should gain ten available messages. Scale the worker back to one replica, watch the queue drain and retrieve the ten job results. The portal can be closed during ingestion and reopened to view the same interval.

## Tear down

```sh
kubectl delete namespace csv-capstone
tofu -chdir=infrastructure destroy -parallelism=1
```

The three managed services have their own lifecycle. Uninstall them when finished; this retains the uploaded objects, pending jobs and results. Permanently purging a service erases all of its course data.
