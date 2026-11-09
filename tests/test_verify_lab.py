"""Safety and failure-path checks for the live verification script (no runtime needed)."""

import argparse
import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    "verify_lab", Path(__file__).resolve().parents[1] / "scripts/verify-lab.py"
)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class RuntimeDetectionTest(unittest.TestCase):
    def test_runtime_detection(self):
        cases = [
            ("docker", "rootful", "", "auto", "docker", ""),
            ("podman", "rootless", "", "auto", "podman", ""),
            ("docker", "offline", "rootless", "auto", "podman", ""),
            ("docker", "rootful", "rootless", "auto", "docker", ""),
            ("docker", "rootful", "rootless", "podman", "podman", ""),
            ("docker", "rootless", "", "auto", "", "Docker rootless"),
            ("podman", "rootful", "", "auto", "", "Podman rootful"),
            ("docker", "alias", "rootless", "auto", "podman", ""),
            ("docker", "offline", "", "docker", "", "Cannot query docker"),
            ("", "", "", "auto", "", "No working Docker or Podman"),
            ("podman", "rootless", "", "docker", "", "Cannot query docker"),
            ("", "", "", "unknown", "", "Unknown runtime"),
        ]
        for primary, mode, podman, requested, expected, error in cases:
            with self.subTest(primary=primary, mode=mode, requested=requested), tempfile.TemporaryDirectory() as directory:
                for name, state in [(primary, mode), ("podman", podman)]:
                    if not name or not state:
                        continue
                    path = Path(directory) / name
                    if state == "offline":
                        body = "exit 1"
                    elif name == "podman" or state == "alias":
                        body = "echo " + ("true" if state != "rootful" else "false")
                    else:
                        security = "rootless" if state == "rootless" else "seccomp"
                        body = (
                            'case "$*" in *Host.Security.Rootless*) exit 1;; esac\n'
                            + "echo '[\"name=" + security + "\"]'"
                        )
                    path.write_text("#!/bin/sh\n" + body + "\n")
                    path.chmod(0o700)
                result = subprocess.run(
                    ["/bin/bash", str(verify.ROOT / "scripts/detect-runtime.sh"), requested],
                    env={**os.environ, "PATH": directory}, capture_output=True, text=True, check=False,
                )
                if error:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(error, result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.strip(), expected)


class VerificationTest(unittest.TestCase):
    def setUp(self):
        self.suite = verify.Verification(
            argparse.Namespace(runtime="podman", cluster="existing", api_url="http://127.0.0.1:8080", timeout=1)
        )
        self.suite.diagnostics = Mock()
        self.addCleanup(shutil.rmtree, self.suite.directory)
        self.addCleanup(self.suite.log.close)

    def test_cli_detects_runtime_before_verification(self):
        for choice in ([], ["--runtime", "podman"]):
            with (
                self.subTest(choice=choice),
                patch.object(verify.sys, "argv", ["verify-lab.py", *choice]),
                patch.dict(os.environ, {"CLOUD_RUNTIME": "auto"}),
                patch.object(verify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "podman\n", "")) as detect,
                patch.object(verify, "Verification") as suite,
            ):
                suite.return_value.execute.return_value = 0
                self.assertEqual(verify.main(), 0)
                self.assertEqual(detect.call_args.args[0][-1], "podman" if choice else "auto")
                self.assertEqual(suite.call_args.args[0].runtime, "podman")
                self.assertEqual(os.environ["CLOUD_RUNTIME"], "podman")

    def test_existing_cluster_is_never_deleted(self):
        self.suite.cloud = Mock()
        self.suite.cleanup()
        self.suite.cloud.assert_not_called()

    def test_namespace_with_changed_ownership_is_never_deleted(self):
        self.suite.namespace_created = True
        self.suite.obj = Mock(return_value={"metadata": {"labels": {verify.LABEL: "someone-else"}}})
        self.suite.k = Mock(return_value=json.dumps({"metadata": {"labels": {verify.LABEL: "someone-else"}}}))
        with self.assertRaisesRegex(RuntimeError, "ownership mismatch"):
            self.suite.cleanup()
        self.assertFalse(any(c.args[0] == "delete" for c in self.suite.k.call_args_list))

    def test_only_created_volume_ids_are_cleaned(self):
        self.suite.volumes.add("pvc-owned")
        self.suite.api = Mock(
            return_value=[{"id": "pvc-owned", "cluster": "existing"}, {"id": "pvc-foreign", "cluster": "other"}]
        )
        self.suite.cloud = Mock()
        self.suite.cleanup()
        self.assertEqual(
            [c.args for c in self.suite.cloud.call_args_list],
            [("volumes", "unpublish", "pvc-owned"), ("volumes", "delete", "pvc-owned")],
        )

    def test_empty_mode_refuses_existing_platform_without_mutation(self):
        self.suite.args.cluster = None
        self.suite.run = Mock(return_value="true")
        self.suite.api = Mock(return_value=[{"name": "existing", "status": "RUNNING"}])
        self.suite.cloud = Mock()
        with (
            patch.object(verify.shutil, "which", return_value="podman"),
            self.assertRaisesRegex(RuntimeError, "--cluster NAME"),
        ):
            self.suite.setup()
        self.assertFalse(self.suite.owned_cluster)
        self.suite.cloud.assert_called_once_with("machine-types", "list")

    def test_failed_check_still_cleans_and_returns_failure(self):
        self.suite.setup = Mock(side_effect=RuntimeError("offline"))
        self.suite.cleanup = Mock()
        self.assertEqual(self.suite.execute(), 1)
        self.suite.cleanup.assert_called_once()
        self.suite.diagnostics.assert_called_once()
        report = json.loads((self.suite.directory / "report.json").read_text())
        self.assertEqual([r["status"] for r in report], ["FAIL", "PASS"])

    def test_skip_does_not_report_success(self):
        self.assertFalse(self.suite.check("autoscaling", Mock(side_effect=verify.Skip("not applicable"))))
        self.assertEqual(self.suite.results[0]["status"], "SKIP")

    def test_kubectl_pins_context_and_disables_tty(self):
        self.suite.run = Mock(return_value="{}")
        self.suite.k("get", "pods")
        argv = self.suite.run.call_args.args[0]
        self.assertIn("-T", argv)
        self.assertIn("--no-deps", argv)
        self.assertEqual(argv[argv.index("--profile") + 1], "tools")
        self.assertLess(argv.index("--profile"), argv.index("run"))
        self.assertEqual(argv[argv.index("--context") + 1], "existing")

    def test_command_failure_preserves_stderr(self):
        with self.assertRaisesRegex(RuntimeError, "deliberate failure"):
            self.suite.run(["sh", "-c", "echo 'deliberate failure' >&2; exit 7"])

    def test_process_timeout_is_bounded(self):
        with self.assertRaises(verify.subprocess.TimeoutExpired):
            self.suite.run(["sleep", "30"], timeout=0.05)

    def test_relay_cleanup_uses_exact_service_uid(self):
        self.suite.apply = Mock()
        self.suite.delete = Mock()
        self.suite.obj = Mock(
            return_value={
                "metadata": {"uid": "owned-uid"},
                "status": {"loadBalancer": {"ingress": [{"ip": "172.30.0.10"}]}},
            }
        )
        self.suite.k = Mock(return_value="Welcome to nginx")
        self.suite.run = Mock(return_value="")
        self.suite.wait = lambda description, predicate, **kwargs: predicate() if "cleanup" in description else None
        self.suite.services()
        for call in self.suite.run.call_args_list:
            self.assertIn("label=minikube-cloud-lab.service-uid=owned-uid", call.args[0])

    def test_full_lifecycle_orders_start_tests_cleanup_and_shutdown(self):
        self.suite.args.cluster = None
        order = []
        methods = [
            "start_platform",
            "setup",
            "limits",
            "workloads",
            "autoscale",
            "storage",
            "retained_storage",
            "services",
            "port_forward",
            "monitoring",
            "cleanup",
            "delete_cluster",
            "shutdown_platform",
        ]
        for name in methods:
            setattr(self.suite, name, Mock(side_effect=lambda name=name: order.append(name)))
        self.assertEqual(self.suite.execute(), 0)
        self.assertEqual(order, methods)

    def test_cluster_cleanup_is_attempted_after_resource_cleanup_fails(self):
        self.suite.args.cluster = None
        self.suite.start_platform = Mock(side_effect=RuntimeError("partial start"))
        self.suite.cleanup = Mock(side_effect=RuntimeError("namespace stuck"))
        self.suite.delete_cluster = Mock()
        self.suite.shutdown_platform = Mock()
        self.assertEqual(self.suite.execute(), 1)
        self.suite.delete_cluster.assert_called_once()
        self.suite.shutdown_platform.assert_called_once()

    def test_shutdown_preserves_foreign_clusters_and_volumes(self):
        self.suite.stop_platform = True
        self.suite.project_label = "io.podman.compose.project=minikube-cloud-lab"
        self.suite.api = Mock(return_value=[{"name": "someone-else"}])
        self.suite.run = Mock(return_value="")
        with self.assertRaisesRegex(RuntimeError, "keeping management"):
            self.suite.shutdown_platform()
        self.suite.run.assert_not_called()
        self.suite.api.return_value = []
        self.suite.shutdown_platform()
        down = self.suite.run.call_args_list[0].args[0]
        self.assertEqual(down[-3:], ["--profile", "tools", "down"])
        self.assertNotIn("-v", down)
        self.assertIn("label=io.podman.compose.project=minikube-cloud-lab", self.suite.run.call_args_list[1].args[0])

    def test_shutdown_rejects_unverified_start(self):
        self.suite.run = Mock()
        with self.assertRaises(verify.Skip):
            self.suite.shutdown_platform()
        self.suite.run.assert_not_called()

    def test_node_leftovers_prevent_successful_cluster_deletion(self):
        self.suite.owned_cluster = True
        self.suite.api = Mock(return_value=[])
        self.suite.run = Mock(return_value=self.suite.cluster + "-m02")
        with self.assertRaisesRegex(RuntimeError, "node containers remain"):
            self.suite.delete_cluster()
        self.assertTrue(self.suite.owned_cluster)

    def test_start_checks_existing_state_before_arming_teardown(self):
        self.suite.run = Mock(return_value="")
        self.suite.wait = Mock()
        self.suite.api = Mock(return_value=[{"name": "existing"}])
        with (
            patch.object(verify.shutil, "which", return_value="podman"),
            self.assertRaisesRegex(RuntimeError, "already exists"),
        ):
            self.suite.start_platform()
        self.assertFalse(self.suite.stop_platform)
        commands = [c.args[0] for c in self.suite.run.call_args_list]
        self.assertEqual(commands[0], ["systemctl", "--user", "start", "podman.socket"])
        self.assertTrue(any(command[-3:] == ["up", "-d", "cloud"] for command in commands))
        self.assertFalse(any("down" in command for command in commands))

    def test_start_records_actual_compose_owner_for_shutdown(self):
        self.suite.wait = Mock()
        self.suite.api = Mock(return_value=[])

        def runtime(argv, **kwargs):
            if "inspect" in argv:
                return json.dumps(
                    [
                        {"State": {"Running": True}, "Config": {"Labels": {"com.docker.compose.service": "lab"}}},
                        {
                            "State": {"Running": True},
                            "Config": {
                                "Labels": {
                                    "io.podman.compose.project": "actual-project",
                                    "com.docker.compose.service": "cloud",
                                }
                            },
                        },
                    ]
                )
            if "ps" in argv:
                self.assertEqual(argv[-2:], ["ps", "-q"], "podman-compose ps does not accept a service argument")
                return "tools-id\ncloud-id"
            return ""

        self.suite.run = Mock(side_effect=runtime)
        with patch.object(verify.shutil, "which", return_value="podman"):
            self.suite.start_platform()
        self.assertTrue(self.suite.stop_platform)
        self.assertEqual(self.suite.project_label, "io.podman.compose.project=actual-project")

    def test_delete_cluster_does_not_delete_unowned_cluster(self):
        self.suite.cloud = Mock()
        self.suite.api = Mock()
        self.suite.delete_cluster()
        self.suite.cloud.assert_not_called()
        self.suite.api.assert_not_called()

    def test_provider_installed_during_configuration_is_used_for_pull(self):
        self.suite.compose = ["podman", "compose", "-f", "compose.rootless.yaml"]
        self.suite.wait = Mock()
        self.suite.api = Mock(return_value=[{"name": "existing"}])
        installed = False
        commands = []

        def run(argv, **kwargs):
            nonlocal installed
            commands.append(argv)
            if argv[0] == "bash":
                installed = True
            return ""

        def which(name):
            return name if name == "podman" or installed else None

        self.suite.run = run
        with (
            patch.object(verify.shutil, "which", side_effect=which),
            self.assertRaisesRegex(RuntimeError, "already exists"),
        ):
            self.suite.start_platform()
        pull = next(argv for argv in commands if argv[-1] == "pull")
        self.assertEqual(pull[0], "podman-compose")


if __name__ == "__main__":
    unittest.main()
