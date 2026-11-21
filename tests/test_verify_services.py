"""Check that service lifecycle commands fail closed without touching a runtime."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/course-service.py"


class ServiceSafetyTest(unittest.TestCase):
    def invoke(self, response, *args):
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / "podman"
            fake.write_text("#!/bin/sh\n" + response)
            fake.chmod(0o700)
            env = dict(os.environ, PATH=tmp + ":" + os.environ["PATH"], HOME=tmp,
                       CLOUD_RUNTIME="podman", COMPOSE_PROJECT_NAME="test-lab")
            return subprocess.run([sys.executable, SCRIPT, *args], env=env, text=True, capture_output=True)

    def test_runtime_failure_is_not_an_empty_inventory(self):
        result = self.invoke("exit 42", "status", "database")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("disabled", result.stdout)

    def test_confirmed_purge_refuses_running_or_stopped_containers(self):
        result = self.invoke("echo existing-id", "purge", "database", "--yes")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("uninstall", result.stderr)

    def test_purge_needs_confirmation(self):
        result = self.invoke("exit 1", "purge", "queue")
        self.assertEqual(result.returncode, 2)
        self.assertIn("--yes", result.stderr)


class ConsoleOwnershipTest(unittest.TestCase):
    def invoke(self, runtime, *argv):
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / "docker"
            fake.write_text("#!/bin/sh\n" + runtime)
            fake.chmod(0o700)
            env = dict(os.environ, PATH=tmp + ":" + os.environ["PATH"], HOME=tmp, CLOUD_RUNTIME="docker")
            return subprocess.run([sys.executable, SCRIPT.with_name("console-module.py"), *argv],
                                  env=env, text=True, capture_output=True)

    def test_failed_runtime_inventory_is_not_disabled(self):
        result = self.invoke("""case "$1" in compose) echo '{"name":"lab-b"}';; *) exit 42;; esac""",
                             "status", "portal")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("disabled", result.stdout)

    def test_purge_refuses_an_unowned_volume(self):
        result = self.invoke("""case "$1 $2" in
            "compose --env-file") echo '{"name":"lab-b"}';;
            "ps -aq") true;;
            "volume ls") echo lab-b_monitoring-history;;
            "volume inspect") echo '[{"Labels":{"com.docker.compose.project":"lab-a"}}]';;
            *) echo 'unexpected mutation' >&2; exit 20;; esac""", "purge", "monitoring", "--yes")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ownership mismatch", result.stderr)

    def test_port_conflict_propagates_without_removing_other_services(self):
        result = self.invoke("""case "$1 $2" in
            "compose --env-file") case "$*" in *config*) echo '{"name":"lab-b"}';; *) echo 'port already allocated' >&2; exit 1;; esac;;
            "ps -aq") true;;
            "ps -q") echo cloud;;
            *) echo 'unexpected cleanup' >&2; exit 23;; esac""", "install", "portal")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("port already allocated", result.stderr)
        self.assertNotIn("unexpected cleanup", result.stderr)
