"""Lifecycle of optional services in the core Compose project."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def inspect_module(runtime, project_filter, name):
    ids = run(runtime, "ps", "-aq", "--filter", project_filter,
              "--filter", "label=com.docker.compose.service=" + name).split()
    if len(ids) > 1:
        raise ValueError("ambiguous module ownership")
    return json.loads(run(runtime, "inspect", ids[0]))[0] if ids else None


def main():
    parser = argparse.ArgumentParser(prog="lab")
    parser.add_argument("action", choices=("status", "install", "uninstall", "purge"))
    parser.add_argument("module", choices=("portal", "monitoring"))
    parser.add_argument("--yes", action="store_true")
    a = parser.parse_args()
    if a.yes != (a.action == "purge") or (a.action == "purge" and a.module != "monitoring"):
        parser.error("only lab purge monitoring --yes erases history")
    rt = os.environ["CLOUD_RUNTIME"]
    if rt == "docker":
        compose = [rt, "compose", "--env-file", str(ROOT / ".env"), "-f", str(ROOT / "compose.yaml")]
    else:
        compose = (["podman-compose"] if shutil.which("podman-compose") else [rt, "compose"])
        compose += ["-f", str(ROOT / "compose.rootless.yaml")]
    compose += ["--profile", a.module]
    lockdir = Path.home() / ".minikube-cloud-lab"
    lockdir.mkdir(mode=0o700, exist_ok=True)
    with (lockdir / (rt + "-console.lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        # Resolve the actual project (including .env and provider normalization).
        # Podman Compose emits YAML; Docker Compose can emit JSON directly.
        if rt == "docker":
            project = json.loads(run(*compose, "config", "--format", "json"))["name"]
        else:
            import yaml
            config = yaml.safe_load(run(*compose, "config"))
            project = config["services"]["cloud"]["labels"]["io.course.console.project"]
        if not project:
            raise ValueError("cannot resolve Compose project")
        project_filter = "label=com.docker.compose.project=" + project
        member = inspect_module(rt, project_filter, a.module)
        retained = False
        volume = project + "_monitoring-history"
        if a.module == "monitoring":
            names = run(rt, "volume", "ls", "-q", "--filter", "name=" + volume).split()
            retained = volume in names
            if retained:
                labels = json.loads(run(rt, "volume", "inspect", volume))[0]["Labels"] or {}
                scope = os.environ.get("CLOUD_COURSE_PROJECT", "mcl")
                if labels.get("com.docker.compose.project") != project or labels.get("io.course.monitoring.scope") != scope:
                    raise ValueError("history volume ownership mismatch")
        if a.action == "purge":
            if member:
                raise ValueError("uninstall monitoring before purge")
            if retained:
                run(rt, "volume", "rm", volume)
            return
        if a.action == "uninstall":
            if member:
                if member["State"]["Running"]:
                    run(rt, "stop", member["Id"])
                run(rt, "rm", member["Id"])
            return
        if a.action == "status" and not member:
            print(a.module, "disabled", "retained" if retained else "empty")
            return
        if a.action == "status" and not member["State"]["Running"]:
            print(a.module, "degraded", "retained" if retained else "empty")
            return
        if a.action == "install":
            if not run(rt, "ps", "-q", "--filter", project_filter, "--filter", "label=com.docker.compose.service=cloud"):
                run(*compose, "up", "-d", "cloud")
            run(*compose, "up", "-d", "--no-deps", a.module)
            member = inspect_module(rt, project_filter, a.module)
            if not member or not member["State"]["Running"]:
                raise ValueError(a.module + " did not start; inspect the Compose logs")
        url = "http://127.0.0.1:" + ("3000/v1/machine-types" if a.module == "portal" else "8080/v1/monitoring/ready")
        for _ in range(45 if a.action == "install" else 1):
            try:
                with urlopen(url, timeout=2) as response:
                    ready = response.status == 200
            except OSError:
                ready = False
            if ready or a.action == "status":
                break
            time.sleep(1)
        if a.action == "install" and not ready:
            raise ValueError(a.module + " readiness timed out")
        print(a.module, "ready" if ready else "degraded", "retained" if a.module == "monitoring" else "empty")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(str(exc))
