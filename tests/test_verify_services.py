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
