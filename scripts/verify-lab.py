#!/usr/bin/env python3
"""Start, verify and stop the lab; --cluster tests an existing deployment in place."""

import argparse
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LABEL = "minikube-cloud-lab.io/verification"


class Skip(Exception):
    pass


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def port_released(port):
    with socket.socket() as sock:
        sock.settimeout(3)
        return sock.connect_ex(("127.0.0.1", port)) != 0


def stop_process(process):
    if process.poll() is None:
        for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGKILL):
            try:
                os.killpg(process.pid, sig)
            except ProcessLookupError:
                break
            try:
                process.wait(timeout=10)
                break
            except subprocess.TimeoutExpired:
                pass


class Verification:
    def __init__(self, args):
        self.args = args
        self.name = "verify-" + uuid.uuid4().hex[:12]
        self.cluster = args.cluster or self.name
        self.owned_cluster = False
        self.stop_platform = False
        self.project_label = None
        self.namespace_created = False
        self.scale_started = False
        self.volumes = set()
        self.created_instances = []
        self.results = []
        self.forward = None
        self.directory = Path(tempfile.mkdtemp(prefix="cloud-lab-verification-"))
        self.log = (self.directory / "commands.log").open("w")
        provider = ["docker", "compose", "--progress", "quiet"]
        if args.runtime == "podman":
            provider = ["podman-compose"] if shutil.which("podman-compose") else ["podman", "compose"]
        filename = "compose.rootless.yaml" if args.runtime == "podman" else "compose.yaml"
        self.compose = (
            provider + ["--project-directory", str(ROOT), "-f", str(ROOT / filename)]
            if args.runtime == "docker"
            else provider + ["-f", str(ROOT / filename)]
        )

    def run(self, argv, data=None, timeout=120):
        self.log.write("$ " + " ".join(argv) + "\n")
        self.log.flush()
        process = subprocess.Popen(
            argv,
            cwd=ROOT,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        )
        try:
            out, err = process.communicate(data, timeout=timeout)
        except BaseException:
            stop_process(process)
            process.communicate()
            raise
        self.log.write(out + err)
        self.log.flush()
        require(process.returncode == 0, f"Command failed ({process.returncode}): {' '.join(argv)}\n{err[-2000:]}")
        return out.strip()

    def cloud(self, *args, timeout=120):
        return self.run(self.compose + ["exec", "-T", "cloud", "cloud", *args], timeout=timeout)

    def k(self, *args, data=None, timeout=120):
        return self.run(
            self.compose
            + [
                "--profile",
                "tools",
                "run",
                "--rm",
                "--no-deps",
                "-T",
                "--entrypoint",
                "kubectl",
                "lab",
                "--context",
                self.cluster,
                *args,
            ],
            data,
            timeout,
        )

    def obj(self, *args):
        return json.loads(self.k("get", *args, "-o", "json"))

    def api(self, path):
        with urllib.request.urlopen("http://127.0.0.1:8080/v1/" + path, timeout=30) as response:
            return json.load(response)

    def wait(self, description, predicate, timeout=None):
        end = time.monotonic() + (timeout or self.args.timeout)
        error = "condition not satisfied"
        while time.monotonic() < end:
            try:
                if predicate():
                    return
            except (RuntimeError, OSError, ValueError) as exc:
                error = str(exc)
            time.sleep(5)
        raise RuntimeError(f"Timeout: {description}: {error}")

    def check(self, name, action):
        print(f"RUN  {name}", flush=True)
        try:
            action()
            status, detail = "PASS", ""
        except Skip as exc:
            status, detail = "SKIP", str(exc)
        except Exception as exc:  # noqa: BLE001 -- report failed checks and continue to cleanup.
            status, detail = "FAIL", str(exc)
        self.results.append({"name": name, "status": status, "detail": detail})
        print(f"{status} {name}" + (f": {detail}" if detail else ""), flush=True)
        return status == "PASS"

    def apply(self, kind, name, spec, api_version="v1"):
        self.k(
            "apply",
            "-f",
            "-",
            data=json.dumps(
                {
                    "apiVersion": api_version,
                    "kind": kind,
                    "metadata": {"name": name, "namespace": self.name, "labels": {LABEL: self.name}},
                    "spec": spec,
                }
            ),
        )

    def ready_nodes(self):
        return [
            n
            for n in self.obj("nodes")["items"]
            if any(c["type"] == "Ready" and c["status"] == "True" for c in n["status"]["conditions"])
        ]

    def start_platform(self):
        require(shutil.which(self.args.runtime), f"Missing {self.args.runtime}")
        if self.args.runtime == "podman":
            self.run(["systemctl", "--user", "start", "podman.socket"])
        script = "configure-rootless.sh" if self.args.runtime == "podman" else "configure.sh"
        self.run(["bash", "-c", 'source "$1"', "verify", str(ROOT / "scripts" / script)], timeout=600)
        # Configuration may have just installed the standalone provider.
        if self.args.runtime == "podman" and shutil.which("podman-compose"):
            self.compose = ["podman-compose", "-f", str(ROOT / "compose.rootless.yaml")]
        if not getattr(self.args, "no_pull", False):
            self.run(self.compose + ["--profile", "tools", "pull"], timeout=1800)
        services = ["cloud"] if self.args.runtime == "podman" else ["cloud", "portal"]
        self.run(self.compose + ["up", "-d", *services], timeout=300)
        self.wait("cloud management API ready", lambda: bool(self.api("machine-types")), timeout=180)
        require(
            not self.api("clusters"),
            "A cluster already exists. Full lifecycle requires an empty platform; use --cluster NAME to test it without shutting it down.",
        )
        require(
            not self.api("instances"),
            "Instances already exist. Full lifecycle requires an empty platform; delete them or use --cluster NAME.",
        )
        # Teardown is authorized only after checking the platform has no existing cluster or instance.
        self.stop_platform = True
        required = ["cloud"] if self.args.runtime == "podman" else ["cloud", "nfs", "portal"]
        # podman-compose ps accepts no service selector; inspect the project's IDs.
        containers = self.run(self.compose + ["ps", "-q"]).split()
        require(bool(containers), "No Compose project containers found")
        records = json.loads(self.run([self.args.runtime, "inspect", *containers]))
        for service in required:
            matches = [
                info
                for info in records
                if (info["Config"].get("Labels") or {}).get("com.docker.compose.service") == service
                or (info["Config"].get("Labels") or {}).get("io.podman.compose.service") == service
            ]
            require(len(matches) == 1, f"Expected one container for {service}, found {len(matches)}")
            info = matches[0]
            if service == "cloud":
                labels = info["Config"].get("Labels", {})
                for key in ("com.docker.compose.project", "io.podman.compose.project"):
                    if labels.get(key):
                        self.project_label = key + "=" + labels[key]
                        break
                require(self.project_label, "Missing Compose ownership label on cloud")
            require(info["State"]["Running"], f"Service {service} is not running")
        if self.args.runtime == "docker":

            def portal_ready():
                with urllib.request.urlopen("http://127.0.0.1:3000", timeout=5) as response:
                    return response.status == 200

            self.wait("portal HTTP ready", portal_ready, timeout=120)

    def delete_cluster(self):
        if self.owned_cluster:
            if any(c["name"] == self.cluster for c in self.api("clusters")):
                self.cloud("clusters", "delete", self.cluster, timeout=900)
            require(not any(c["name"] == self.cluster for c in self.api("clusters")), "Temporary cluster still exists")
            names = self.run([self.args.runtime, "ps", "-a", "--format", "{{.Names}}"]).splitlines()
            require(
                not any(n == self.cluster or n.startswith(self.cluster + "-m") for n in names),
                "Test node containers remain",
            )
            relays = self.run(
                [
                    self.args.runtime,
                    "ps",
                    "-aq",
                    "--filter",
                    "label=minikube-cloud-lab.service-proxy=true",
                    "--filter",
                    "label=minikube-cloud-lab.cluster=" + self.cluster,
                ]
            )
            require(not relays, "Test service relay containers remain")
            self.owned_cluster = False

    def shutdown_platform(self):
        if not self.stop_platform:
            raise Skip("Platform not confirmed empty; preserved to protect existing state")
        require(
            not self.owned_cluster and not self.api("clusters"),
            "Cluster deletion did not complete; keeping management services available for recovery",
        )
        require(self.project_label, "Cannot verify project ownership for teardown")
        self.run(self.compose + ["--profile", "tools", "down"], timeout=300)
        remaining = self.run([self.args.runtime, "ps", "-aq", "--filter", "label=" + self.project_label])
        require(not remaining, "Compose project containers remain: " + remaining)

    def setup(self):
        require(shutil.which(self.args.runtime), f"Missing {self.args.runtime}")
        if self.args.runtime == "podman":
            require(
                self.run(["podman", "info", "--format", "{{.Host.Security.Rootless}}"]).strip() == "true",
                "Podman must be rootless",
            )
        self.cloud("machine-types", "list")
        clusters = self.api("clusters")
        if self.args.cluster:
            require(
                any(c["name"] == self.cluster and c["status"] == "RUNNING" for c in clusters),
                "Selected cluster is not RUNNING",
            )
        else:
            require(
                not clusters, "Platform already has a cluster. Use --cluster NAME; existing clusters are never deleted."
            )
            self.owned_cluster = True
            self.cloud("clusters", "create", self.cluster, "--min-nodes", "1", "--max-nodes", "2", timeout=1800)
        # The private kubeconfig already contains explicit cluster contexts; do not change current-context.
        self.wait("Ready control plane", lambda: bool(self.ready_nodes()))
        nodes = self.ready_nodes()
        self.control = next(
            n["metadata"]["name"] for n in nodes if "node-role.kubernetes.io/control-plane" in n["metadata"]["labels"]
        )
        # Set before creating: cleanup must also cover a create interrupted after the API accepted it.
        self.namespace_created = True
        self.k(
            "create",
            "-f",
            "-",
            data=json.dumps(
                {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": self.name, "labels": {LABEL: self.name}}}
            ),
        )

    def limits(self):
        shapes = {s["name"]: s for s in self.api("machine-types")}
        for node in self.ready_nodes():
            shape = shapes[node["metadata"]["labels"]["node.kubernetes.io/instance-type"]]
            name = node["metadata"]["name"]
            values = self.run(
                [self.args.runtime, "exec", name, "sh", "-c", "cat /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max"]
            ).split()
            require(
                len(values) == 3 and values[0] != "max" and values[2] != "max",
                f"Missing cgroup v2 limits on {name}: {values}",
            )
            require(int(values[0]) == shape["cpus"] * int(values[1]), f"Wrong CPU limit on {name}: {values}")
            require(int(values[2]) == shape["memoryMiB"] * 1024**2, f"Wrong memory limit on {name}: {values}")

    def workloads(self):
        self.apply(
            "Deployment",
            "web",
            {
                "replicas": 1,
                "selector": {"matchLabels": {"app": self.name}},
                "template": {
                    "metadata": {"labels": {"app": self.name}},
                    "spec": {
                        "nodeName": self.control,
                        "containers": [
                            {
                                "name": "web",
                                "image": "nginx:1.28-alpine",
                                "resources": {"requests": {"cpu": "10m", "memory": "32Mi"}},
                            }
                        ],
                    },
                },
            },
            "apps/v1",
        )
        self.k("-n", self.name, "rollout", "status", "deployment/web", "--timeout=5m", timeout=330)
        self.apply("Service", "web", {"selector": {"app": self.name}, "ports": [{"port": 80, "targetPort": 80}]})
        self.apply(
            "Pod",
            "client",
            {
                "nodeName": self.control,
                "containers": [{"name": "client", "image": "busybox:1.37.0", "command": ["sleep", "3600"]}],
            },
        )
        self.k("-n", self.name, "wait", "--for=condition=Ready", "pod/client", "--timeout=5m", timeout=330)
        self.k("-n", self.name, "exec", "client", "--", "nslookup", "kubernetes.default.svc.cloudlab.internal")
        html = self.k("-n", self.name, "exec", "client", "--", "wget", "-qO-", "http://web")
        require("Welcome to nginx" in html, "ClusterIP/DNS HTTP response mismatch")

    def autoscale(self):
        cluster = next(c for c in self.api("clusters") if c["name"] == self.cluster)
        if (
            cluster["minMachines"] != 1
            or cluster["maxMachines"] < 2
            or cluster["machineType"] != "lab-small"
            or len(self.obj("nodes")["items"]) != 1
        ):
            raise Skip("1 -> 2 -> 1 requires a lab-small cluster initially at one node, min=1 and max>=2")
        if self.owned_cluster:
            self.run(
                self.compose
                + [
                    "--profile",
                    "tools",
                    "run",
                    "--rm",
                    "--no-deps",
                    "-T",
                    "lab",
                    "--context",
                    self.cluster,
                    "install",
                    "autoscaler",
                ]
            )
        elif not self.obj("deployments", "-n", "kube-system", "-l", "app=cluster-autoscaler")["items"]:
            raise Skip("Install the autoscaler on the existing cluster first: lab install autoscaler")
        self.k("-n", "kube-system", "rollout", "status", "deployment/cluster-autoscaler", "--timeout=5m", timeout=330)
        self.scale_started = True
        self.apply(
            "Deployment",
            "load",
            {
                "replicas": 4,
                "selector": {"matchLabels": {"load": self.name}},
                "template": {
                    "metadata": {"labels": {"load": self.name}},
                    "spec": {
                        "containers": [
                            {
                                "name": "pause",
                                "image": "registry.k8s.io/pause:3.10",
                                "resources": {
                                    "requests": {"cpu": "600m", "memory": "64Mi"},
                                    "limits": {"cpu": "600m", "memory": "64Mi"},
                                },
                            }
                        ]
                    },
                },
            },
            "apps/v1",
        )
        try:
            self.wait("autoscaling to two Ready nodes", lambda: len(self.ready_nodes()) == 2)
            self.k("-n", self.name, "rollout", "status", "deployment/load", "--timeout=5m", timeout=330)
            pods = self.obj("pods", "-n", self.name, "-l", "load=" + self.name)["items"]
            require(len({p["spec"]["nodeName"] for p in pods}) == 2, "Load pods did not run on both nodes")
            self.limits()
            worker = next(n["metadata"]["name"] for n in self.ready_nodes() if n["metadata"]["name"] != self.control)
            self.apply(
                "Pod",
                "worker-client",
                {
                    "nodeName": worker,
                    "containers": [{"name": "client", "image": "busybox:1.37.0", "command": ["sleep", "3600"]}],
                },
            )
            try:
                self.k(
                    "-n", self.name, "wait", "--for=condition=Ready", "pod/worker-client", "--timeout=5m", timeout=330
                )
                html = self.k("-n", self.name, "exec", "worker-client", "--", "wget", "-qO-", "http://web")
                require("Welcome to nginx" in html, "Cross-node DNS/Service network failed")
            finally:
                self.delete("pod/worker-client")
        finally:
            self.k("-n", self.name, "delete", "deployment/load", "--ignore-not-found", "--timeout=60s")
        self.wait("autoscaling back to one node", lambda: len(self.obj("nodes")["items"]) == 1)
        require(self.ready_nodes()[0]["metadata"]["name"] == self.control, "Control plane was not retained")

    def claim(self, name, storage_class, capacity, volume=None):
        spec = {
            "storageClassName": storage_class,
            "accessModes": ["ReadWriteOnce"],
            "resources": {"requests": {"storage": capacity}},
        }
        if volume:
            spec["volumeName"] = volume
        self.apply("PersistentVolumeClaim", name, spec)

    def bound(self, name):
        self.k(
            "-n", self.name, "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/" + name, "--timeout=5m", timeout=330
        )
        volume = self.obj("pvc", name, "-n", self.name)["spec"]["volumeName"]
        self.volumes.add(volume)
        return volume

    def writer(self, claim):
        self.apply(
            "Pod",
            "writer",
            {
                "nodeName": self.control,
                "containers": [
                    {
                        "name": "writer",
                        "image": "busybox:1.37.0",
                        "command": ["sleep", "3600"],
                        "volumeMounts": [{"name": "data", "mountPath": "/data"}],
                    }
                ],
                "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": claim}}],
            },
        )
        self.k("-n", self.name, "wait", "--for=condition=Ready", "pod/writer", "--timeout=5m", timeout=330)

    def delete(self, resource):
        self.k("-n", self.name, "delete", resource, "--ignore-not-found", "--timeout=120s", timeout=150)

    def storage(self):
        self.claim("data", "cloud-standard", "64Mi")
        volume = self.bound("data")
        self.writer("data")
        # Directory mutations followed by getattr expose Ganesha's Linux 6.19+ incompatibility.
        self.k(
            "-n", self.name, "exec", "writer", "--", "sh", "-ec",
            "mkdir /data/dir-probe; i=0; while [ $i -lt 20 ]; do "
            "touch /data/dir-probe/file; stat /data/dir-probe >/dev/null; "
            "rm /data/dir-probe/file; stat /data/dir-probe >/dev/null; "
            "i=$((i+1)); done; rmdir /data/dir-probe",
        )
        self.k("-n", self.name, "exec", "writer", "--", "sh", "-c", f"printf '%s' '{self.name}' > /data/proof; sync")
        self.delete("pod/writer")
        self.writer("data")
        require(
            self.k("-n", self.name, "exec", "writer", "--", "cat", "/data/proof") == self.name, "Persistence failed"
        )
        # A request for the total budget cannot fit while the accepted PVC reserves 64Mi.
        self.claim("oversized", "cloud-standard", self.api("storage")["total"])
        self.wait(
            "StorageCapacityExceeded event",
            lambda: any(
                e.get("reason") == "StorageCapacityExceeded" and e["involvedObject"]["name"] == "oversized"
                for e in self.obj("events", "-n", self.name)["items"]
            ),
        )
        claim = self.obj("pvc", "oversized", "-n", self.name)
        require(
            claim["status"]["phase"] == "Pending" and not claim["spec"].get("volumeName"),
            "Oversized PVC unexpectedly provisioned",
        )
        self.delete("pod/writer")
        self.delete("pvc/data")
        self.delete("pvc/oversized")
        self.wait("Delete volume releases reservation", lambda: not any(v["id"] == volume for v in self.api("volumes")))
        self.volumes.discard(volume)

    def retained_storage(self):
        volume = self.cloud("volumes", "create", "--capacity", "64Mi").strip()
        require(volume.startswith("pvc-"), f"Unexpected volume ID: {volume}")
        self.volumes.add(volume)
        for attempt in range(2):
            self.cloud("volumes", "publish", volume, "--cluster", self.cluster)
            self.claim("retained", "cloud-static", "64Mi", volume)
            self.bound("retained")
            self.writer("retained")
            if attempt == 0:
                self.k(
                    "-n",
                    self.name,
                    "exec",
                    "writer",
                    "--",
                    "sh",
                    "-c",
                    f"printf '%s' '{self.name}' > /data/proof; sync",
                )
            else:
                require(
                    self.k("-n", self.name, "exec", "writer", "--", "cat", "/data/proof") == self.name,
                    "Retained data lost after republish",
                )
            self.delete("pod/writer")
            self.delete("pvc/retained")
            self.wait("Retain release", lambda: self.obj("pv", volume)["status"]["phase"] == "Released")
            self.wait("Unpublish retained volume", lambda: self.cloud("volumes", "unpublish", volume) is not None)
        self.cloud("volumes", "delete", volume)
        self.wait("Retain explicit deletion", lambda: not any(v["id"] == volume for v in self.api("volumes")))
        self.volumes.discard(volume)

    def http(self, port):
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=5) as response:
            return b"Welcome to nginx" in response.read(65536)

    def services(self):
        for kind in ("NodePort", "LoadBalancer"):
            name = kind.lower()
            self.apply(
                "Service",
                name,
                {"type": kind, "selector": {"app": self.name}, "ports": [{"port": 80, "targetPort": 80}]},
            )

            def published(name=name):
                service = self.obj("service", name, "-n", self.name)
                endpoints = json.loads(
                    service["metadata"].get("annotations", {}).get("minikube-cloud-lab.io/host-endpoints", "[]")
                )
                return bool(endpoints) and all(self.http(e["hostPort"]) for e in endpoints)

            self.wait(kind + " HTTP publication", published)
            service = self.obj("service", name, "-n", self.name)
            if kind == "LoadBalancer":
                ip = service["status"]["loadBalancer"]["ingress"][0]["ip"]
                html = self.k("-n", self.name, "exec", "client", "--", "wget", "-qO-", "http://" + ip)
                require("Welcome to nginx" in html, "LoadBalancer internal IP failed")
            uid = service["metadata"]["uid"]
            self.delete("service/" + name)
            self.wait(
                kind + " relay cleanup",
                lambda uid=uid: (
                    not self.run(
                        [
                            self.args.runtime,
                            "ps",
                            "-aq",
                            "--filter",
                            "label=minikube-cloud-lab.service-proxy=true",
                            "--filter",
                            "label=minikube-cloud-lab.cluster=" + self.cluster,
                            "--filter",
                            "label=minikube-cloud-lab.service-uid=" + uid,
                        ]
                    )
                ),
            )

    def port_forward(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        script = "configure-rootless.sh" if self.args.runtime == "podman" else "configure.sh"
        argv = [
            "bash",
            "-c",
            'source "$1" >/dev/null && kubectl --context "$2" -n "$3" port-forward service/web "$4:80"',
            "verify",
            str(ROOT / "scripts" / script),
            self.cluster,
            self.name,
            str(port),
        ]
        with (self.directory / "port-forward.log").open("w") as log:
            self.forward = subprocess.Popen(argv, cwd=ROOT, stdout=log, stderr=log, start_new_session=True)
            try:
                self.wait("port-forward HTTP", lambda: self.forward.poll() is None and self.http(port), timeout=90)
            finally:
                stop_process(self.forward)
                self.forward = None
        # Docker Desktop releases the published port shortly after the forward container exits.
        self.wait("port-forward still listening after shutdown", lambda: port_released(port), timeout=30)

    def monitoring(self):
        self.wait(
            "monitoring metrics",
            lambda: (
                self.api("clusters/" + self.cluster + "/monitoring").get("coveredNodes", 0) == len(self.ready_nodes())
            ),
        )
        self.api("clusters/" + self.cluster + "/dashboard")

    def instances(self):
        names = [f"{self.name}-a", f"{self.name}-b"]
        require(all(len(name) <= 63 for name in names), "Instance names exceed 63 characters")
        for name in names:
            self.created_instances.append(name)
            self.cloud("instances", "create", name, timeout=600)
        first, second = names

        def ssh(*command, timeout=300):
            return self.cloud("instances", "ssh", first, "--", *command, timeout=timeout)

        peer = self.api("instances/" + second)["ip"]
        require("Hello from Docker!" in ssh("sudo", "docker", "run", "--rm", "hello-world", timeout=600), "Nested Docker did not run hello-world")
        ssh("bash", "-c", f"'</dev/tcp/{peer}/22'")
        ssh("curl", "-fsS", "-o", "/dev/null", "https://deb.debian.org")
        ssh("sh", "-c", "'echo persisted > ~/mcl-verify'")
        self.cloud("instances", "stop", first, timeout=300)
        self.cloud("instances", "start", first, timeout=300)
        require(ssh("cat", "~/mcl-verify") == "persisted", "File in /home lost across stop/start")
        for name in names:
            self.cloud("instances", "delete", name, timeout=300)
            self.created_instances.remove(name)
            left = self.run([self.args.runtime, "ps", "-aq", "--filter", "label=minikube-cloud-lab.instance=" + name])
            require(not left, f"Instance container {name} left behind")
            volumes = self.run([self.args.runtime, "volume", "ls", "-q", "--filter", "label=minikube-cloud-lab.instance=" + name])
            require(not volumes, f"Instance volume {name} left behind")

    def diagnostics(self):
        try:
            (self.directory / "cloud.log").write_text(self.run(self.compose + ["logs", "--tail=150", "cloud"]))
            names = self.run([self.args.runtime, "ps", "-a", "--format", "{{.Names}}"]).splitlines()
            for name in names:
                if name == self.cluster or name.startswith(self.cluster + "-m"):
                    (self.directory / (name + ".log")).write_text(
                        self.run([self.args.runtime, "logs", "--tail=150", name])
                    )
                    (self.directory / (name + "-state.json")).write_text(
                        self.run([self.args.runtime, "inspect", "--format", "{{json .State}}", name])
                    )
        except Exception as exc:  # noqa: BLE001 -- diagnostics must not prevent cleanup.
            self.log.write(f"Runtime diagnostics: {exc}\n")
        if not self.namespace_created:
            return
        for name, command in [
            ("nodes", ["nodes"]),
            ("pods", ["pods", "-A"]),
            ("events", ["events", "-n", self.name]),
            ("storage", ["pvc", "-n", self.name]),
        ]:
            try:
                (self.directory / (name + ".json")).write_text(json.dumps(self.obj(*command), indent=2))
            except Exception as exc:  # noqa: BLE001 -- diagnostics must not prevent cleanup.
                self.log.write(f"Diagnostics {name}: {exc}\n")
        try:
            (self.directory / "autoscaler.log").write_text(
                self.k("-n", "kube-system", "logs", "deployment/cluster-autoscaler", "--tail=150")
            )
        except Exception as exc:  # noqa: BLE001 -- diagnostics must not prevent cleanup.
            self.log.write(f"Autoscaler diagnostics: {exc}\n")

    def cleanup(self):
        if self.forward:
            stop_process(self.forward)
        # Keep cleaning the namespace and volumes even if an instance cannot be deleted yet.
        instance_failures = []
        for name in list(self.created_instances):
            try:
                self.cloud("instances", "delete", name, timeout=300)
            except RuntimeError as exc:
                if "not found" not in str(exc):
                    instance_failures.append(str(exc))
                    continue
            self.created_instances.remove(name)
        if self.namespace_created:
            raw = self.k("get", "namespace", self.name, "--ignore-not-found", "-o", "json")
            namespace = json.loads(raw or "{}")
            if namespace:
                require(
                    namespace["metadata"].get("labels", {}).get(LABEL) == self.name,
                    "Refusing cleanup: namespace ownership mismatch",
                )
                # Include PVCs allocated before an interrupted Bound check.
                for pvc in self.obj("pvc", "-n", self.name)["items"]:
                    if pvc["spec"].get("volumeName"):
                        self.volumes.add(pvc["spec"]["volumeName"])
                self.k("delete", "namespace", self.name, "--timeout=180s", timeout=210)
            self.namespace_created = False
        for volume in list(self.volumes):

            def released(volume=volume):
                item = next((v for v in self.api("volumes") if v["id"] == volume), None)
                if item is None:
                    return True
                require(not item.get("cluster") or item["cluster"] == self.cluster, "Volume ownership changed")
                self.cloud("volumes", "unpublish", volume)
                self.cloud("volumes", "delete", volume)
                return True

            self.wait("cleanup " + volume, released, timeout=180)
            self.volumes.discard(volume)
        if self.scale_started and not self.owned_cluster:
            self.wait(
                "restore initial node count after test load removal", lambda: len(self.obj("nodes")["items"]) == 1
            )
        require(not instance_failures, "Instance cleanup failed: " + "; ".join(instance_failures))

    def execute(self):
        print(f"Cluster: {self.cluster}; namespace: {self.name}; logs: {self.directory}", flush=True)
        try:
            started = bool(self.args.cluster) or self.check("start platform and verify services", self.start_platform)
            if started and self.check("platform / cluster / namespace", self.setup):
                for name, action in [
                    ("CPU and RAM limits", self.limits),
                    ("deployment, DNS and ClusterIP", self.workloads),
                    ("autoscaling 1 -> 2 -> 1", self.autoscale),
                    ("PVC persistence, budget and Delete", self.storage),
                    ("static provisioning and Retain", self.retained_storage),
                    ("NodePort / LoadBalancer HTTP", self.services),
                    ("port-forward and shutdown", self.port_forward),
                    ("monitoring and dashboard API", self.monitoring),
                    ("standalone instances: SSH, Docker, network, persistence", self.instances),
                ]:
                    self.check(name, action)
        except KeyboardInterrupt:
            self.results.append({"name": "interrupted", "status": "FAIL", "detail": "Interrupted by user"})
        finally:
            if any(r["status"] == "FAIL" for r in self.results):
                self.diagnostics()
            self.check("cleanup (only test resources)", self.cleanup)
            if not self.args.cluster:
                self.check("delete temporary cluster and node containers", self.delete_cluster)
                self.check("stop all lab project containers", self.shutdown_platform)
            (self.directory / "report.json").write_text(json.dumps(self.results, indent=2))
            self.log.close()
        counts = {s: sum(r["status"] == s for r in self.results) for s in ("PASS", "FAIL", "SKIP")}
        print(f"Result: {counts}; report: {self.directory / 'report.json'}")
        return 1 if counts["FAIL"] else 2 if counts["SKIP"] else 0


def main():
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog="Exit: 0 all PASS; 1 FAIL; 2 incomplete (SKIP). Existing workloads are preserved; autoscaling adds temporary load. Budget 20-40 minutes and capacity for two lab-small nodes.",
    )
    parser.add_argument(
        "--runtime",
        choices=("auto", "podman", "docker"),
        default=os.environ.get("CLOUD_RUNTIME", "auto"),
        help="Runtime (default: auto; Docker first, then rootless Podman)",
    )
    parser.add_argument(
        "--cluster",
        help="Optional: test an existing running cluster without starting/stopping the platform. Default: full lifecycle with a temporary cluster",
    )
    parser.add_argument("--timeout", type=int, default=600, help="Polling timeout per check in seconds (default: 600)")
    parser.add_argument(
        "--no-pull",
        action="store_true",
        help="Skip pulling images, to verify locally built images set in .env; missing images are still pulled on start",
    )
    args = parser.parse_args()
    if args.timeout < 1:
        parser.error("--timeout must be positive")
    os.environ["PATH"] = str(Path.home() / ".local/bin") + os.pathsep + os.environ.get("PATH", "")
    try:
        detected = subprocess.run(
            ["bash", str(ROOT / "scripts/detect-runtime.sh"), args.runtime],
            capture_output=True, text=True, timeout=60, check=False,
        )
        if detected.returncode:
            parser.error(detected.stderr.strip())
        args.runtime = detected.stdout.strip()
    except (OSError, subprocess.TimeoutExpired) as exc:
        parser.error(f"Runtime detection failed: {exc}")
    print(f"Detected runtime: {args.runtime}", flush=True)
    os.environ["CLOUD_RUNTIME"] = args.runtime
    return Verification(args).execute()


if __name__ == "__main__":
    sys.exit(main())
