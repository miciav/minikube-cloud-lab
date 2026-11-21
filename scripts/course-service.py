#!/usr/bin/env python3
"""Manage one optional Compose project in the configured course environment."""
import argparse
import fcntl
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
PORTS = {"object-storage": 3900, "queue": 9324, "database": 15432}
AWS = "docker.io/amazon/aws-cli:2.27.49@sha256:1b7003e3ecb737b7533d8e16a547b4bc36e912508c3532ae51003aad8d0cd41d"


def write(path, value, mode=0o600):
    with tempfile.NamedTemporaryFile(dir=path.parent, mode="w", delete=False) as file:
        file.write(value)
    os.chmod(file.name, mode)
    os.replace(file.name, path)


def main():
    parser = argparse.ArgumentParser(prog="lab")
    parser.add_argument("action", choices=["install", "status", "uninstall", "purge", "client"])
    parser.add_argument("module", choices=PORTS)
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    name, action = args.module, args.action
    if args.args in (["--help"], ["-h"]):
        parser.print_help()
        return
    if action == "purge" and args.args != ["--yes"]:
        parser.error("purge requires --yes")
    if action not in ("purge", "client") and args.args:
        parser.error("unexpected arguments")
    runtime = os.environ["CLOUD_RUNTIME"]
    project = os.environ.get("COMPOSE_PROJECT_NAME", "mcl") + "-" + name
    if not re.fullmatch("[a-z0-9][a-z0-9_-]*", project):
        parser.error("invalid Compose project name")
    base = Path.home() / ".minikube-cloud-lab" / "services" / runtime
    base.mkdir(mode=0o700, parents=True, exist_ok=True)
    state = base / project
    port = int(os.getenv(name.upper().replace("-", "_") + "_PORT", PORTS[name]))
    if not 1024 <= port <= 65535:
        parser.error("choose an unprivileged port from 1024 to 65535")
    env = dict(os.environ, SERVICE_STATE=str(state), SERVICE_PORT=str(port))
    compose = (["podman-compose"] if runtime == "podman" and shutil.which("podman-compose")
               else [runtime, "compose"]) + ["-p", project, "-f", str(ROOT / "lab/services" / name / "compose.yaml")]

    def cmd(argv, capture=False, check=True):
        return subprocess.run(argv, text=True, env=env, check=check,
                              stdout=subprocess.PIPE if capture else None,
                              stderr=subprocess.PIPE if capture else None)

    def inside(*argv, **kw):
        return cmd(compose + ["exec", "-T", name, *argv], **kw)

    def garage(*argv, **kw):
        return inside("/garage", *argv, **kw)

    def inventory(kind):
        verb = ["ps", "-aq"] if kind == "containers" else ["volume", "ls", "-q"]
        filters = ["--filter", "label=com.docker.compose.project=" + project]
        if kind == "containers":
            filters += ["--filter", "label=com.docker.compose.service=" + name]
        return cmd([runtime, *verb, *filters], True).stdout.split()

    def probe():
        if name == "queue":
            with urlopen(f"http://127.0.0.1:{port}/?Action=ListQueues&Version=2012-11-05", timeout=3) as r:
                return b"ListQueues" in r.read()
        if name == "database":
            return inside("sh", "-ec", 'PGPASSWORD=$(cat /run/bootstrap/student) psql -h localhost -U student -d course -Atc "SELECT 1"',
                          capture=True, check=False).stdout.strip() == "1"
        return ((state / "student").is_file() and (state / "readonly").is_file()
                and garage("bucket", "info", "course-files", capture=True, check=False).returncode == 0)

    def wait(check):
        for _ in range(90):
            try:
                if check():
                    return
            except OSError:
                pass
            time.sleep(1)
        raise RuntimeError(f"{name} readiness timed out; inspect the service logs")

    with (base / (project + ".lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        containers = inventory("containers")
        volumes = inventory("volumes")
        if action == "status":
            try:
                ready = bool(containers) and probe()
            except OSError:
                ready = False
            print(name, ("ready" if ready else "degraded") if containers else "disabled",
                  "retained" if volumes or state.exists() else "empty")
            return
        if action == "purge":
            if containers:
                raise RuntimeError("uninstall the module before purge")
            if any(v != project + "_data" for v in volumes):
                raise RuntimeError("unexpected project volume; refusing purge")
            if volumes:
                cmd([runtime, "volume", "rm", *volumes])
            if state.exists():
                shutil.rmtree(state)
            return
        if action == "uninstall":
            if containers:
                cmd(compose + ["down"])
            return
        if action == "client":
            fcntl.flock(lock, fcntl.LOCK_UN)
            if len(containers) != 1:
                raise RuntimeError("expected one installed service container")
            network = project + "_default"
            if name == "database":
                image = cmd([runtime, "inspect", "--format", "{{.Image}}", containers[0]], True).stdout.strip()
                env["PGPASSWORD"] = (state / "student").read_text().strip()
                cmd([runtime, "run", "--rm", "-i", "--network", network, "-e", "PGPASSWORD",
                     "--entrypoint", "psql", image, "-h", name, "-U", "student", "-d", "course", *args.args])
                return
            mode = "readonly" if args.args[:1] == ["--read-only"] else "student"
            argv = args.args[1:] if mode == "readonly" else args.args
            access, secret = (state / mode).read_text().split() if name == "object-storage" else ("course", "course")
            env.update(AWS_ACCESS_KEY_ID=access, AWS_SECRET_ACCESS_KEY=secret)
            cmd([runtime, "run", "--rm", "-i", "--network", network,
                 "-e", "AWS_ACCESS_KEY_ID", "-e", "AWS_SECRET_ACCESS_KEY", AWS, "--region", "garage",
                 "--endpoint-url", f"http://{name}:{PORTS[name]}", *argv])
            return

        if volumes and not state.is_dir():
            raise RuntimeError("retained volume has no matching client configuration")
        state.mkdir(mode=0o700, exist_ok=True)
        if name == "database":
            for role in ("admin", "student"):
                if not (state / role).exists():
                    if volumes:
                        raise RuntimeError("database credential missing")
                    write(state / role, secrets.token_hex(24), 0o444)
        if name == "object-storage" and not (state / "garage.toml").exists():
            if volumes:
                raise RuntimeError("Garage configuration missing")
            write(state / "garage.toml", f'''metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "127.0.0.1:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "{secrets.token_hex(32)}"
[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
''')
        cmd(compose + ["up", "-d", name])
        if name == "object-storage":
            wait(lambda: garage("status", capture=True, check=False).returncode == 0)
            layout = garage("layout", "show", capture=True).stdout
            if "Current cluster layout version: 0" in layout:
                node = garage("node", "id", "-q", capture=True).stdout.strip().split("@")[0]
                garage("layout", "assign", node, "-z", "lab", "-c", "1G")
                garage("layout", "apply", "--version", "1")
            if garage("bucket", "info", "course-files", capture=True, check=False).returncode:
                garage("bucket", "create", "course-files")
            for role in ("student", "readonly"):
                if garage("key", "info", role, capture=True, check=False).returncode:
                    garage("key", "create", role, capture=True)
                info = garage("key", "info", "--show-secret", role, capture=True).stdout
                access = re.search(r"Key ID:\s*(\S+)", info).group(1)
                secret = re.search(r"Secret key:\s*(\S+)", info).group(1)
                write(state / role, f"{access}\n{secret}\n")
                garage("bucket", "allow", "--read", *(["--write"] if role == "student" else []),
                       "--key", role, "course-files", capture=True)
        wait(probe)
        print(f"{name} ready at 127.0.0.1:{port}. Use lab client {name}. Configuration: {state}")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(str(exc))
