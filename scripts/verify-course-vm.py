#!/usr/bin/env python3
"""Run the course acceptance suites inside the IT-provided rootless Podman VM."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
SHELL = ["bash", "-ec", 'source scripts/configure.sh; "$@"', "course-check"]


def api(resource):
    with urlopen("http://127.0.0.1:8080/v1/" + resource, timeout=10) as response:
        return json.load(response)


def check_empty():
    for resource in ("clusters", "instances", "groups", "volumes", "disks"):
        if api(resource) not in (None, []):
            raise RuntimeError(f"Found existing {resource}. Run on an empty course installation.")
    if any(op["status"] == "RUNNING" for op in (api("operations") or [])):
        raise RuntimeError("A cloud operation is still running.")
    if api("instance-autoscaling") != {"state": "disabled", "remaining": []}:
        raise RuntimeError("The instance autoscaling module must initially be disabled.")


def verify(run):
    run("Start user socket", ["systemctl", "--user", "start", "podman.socket"])
    run("Check runner prerequisites", ["bash", "-ec", "command -v zsh; python3 -c 'import yaml'"])
    run("Verification script tests", [sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", "test_verify*.py"])
    run("Lecture 02 manifest and shell checks", [sys.executable, "kubernetes/lecture02/validate.py"])
    run("Start course cloud", SHELL + ["labcompose", "up", "-d", "--no-recreate", "cloud"])
    run("Wait for course API", ["curl", "--retry", "60", "--retry-delay", "2", "--retry-all-errors",
                               "--max-time", "5", "-fsS", "http://127.0.0.1:8080/v1/machine-types"])
    check_empty()
    run("Portal and monitoring lifecycle", ["bash", "tests/monitoring.sh"])
    run("Independent course services and CSV worker", ["bash", "tests/managed-services.sh"])
    for image in ("ubuntu-26.04", "debian-12"):
        run("Cloud-init and SSH: " + image, ["env", "RUNTIME=podman", "TEST_CLOUD_CONTAINER=",
                                             "bash", "tests/instance-cloud-init.sh", image])
    run("Persistent disk across instances", [sys.executable, "tests/rootless-disk-console.py"])
    run("Traffic scaling, replacement and module removal", ["bash", "tests/instance-autoscaling.sh"])
    check_empty()
    run("Mixed VM and Kubernetes capstone", ["bash", "tests/capstone.sh"])
    check_empty()
    run("Lecture 03 live exercises", [sys.executable, "scripts/verify-lecture03.py", "--runtime", "podman",
                                      "--recreate-empty-cluster"])
    check_empty()
    run("Kubernetes, networking, storage, instances and shutdown",
        [sys.executable, "scripts/verify-lab.py", "--runtime", "podman", "--no-pull"])


def main():
    argparse.ArgumentParser(description=__doc__).parse_args()
    base = Path(os.environ.get("XDG_STATE_HOME") or Path.home() / ".local/state") / "minikube-cloud-lab"
    base.mkdir(parents=True, exist_ok=True)
    with (base / "course-vm.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            sys.exit("Another course verification is running.")
        directory = Path(tempfile.mkdtemp(prefix="course-vm-", dir=base))
        os.environ.update(CLOUD_RUNTIME="podman", TMPDIR=str(directory), PYTHONUNBUFFERED="1")
        print(f"Report directory: {directory}", flush=True)
        steps = []

        def run(name, command):
            print(f"RUN  {name}", flush=True)
            with (directory / f"{len(steps) + 1:02d}.log").open("w") as log:
                code = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT).returncode
            steps.append({"step": name, "exit_code": code, "log": log.name})
            if code:
                raise RuntimeError(f"{name} exited with {code}. See {log.name}")
            print(f"PASS {name}", flush=True)

        error = None
        try:
            verify(run)
        except (RuntimeError, OSError, ValueError, subprocess.SubprocessError, KeyboardInterrupt) as exc:
            error = str(exc) or "Interrupted"
        status = "FAIL" if error else "PASS"
        report = directory / "report.json"
        report.write_text(json.dumps(dict(status=status, error=error, steps=steps), indent=2) + "\n")
        print(f"{status}: {error or 'Course VM checks completed'}\nReport: {report}")
        return int(bool(error))


if __name__ == "__main__":
    sys.exit(main())
