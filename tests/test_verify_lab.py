"""Safety and failure-path checks for the live verification script (no runtime needed)."""

import argparse
import importlib.util
import json
import shutil
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    "verify_lab", Path(__file__).resolve().parents[1] / "scripts/verify-lab.py"
)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class VerificationTest(unittest.TestCase):
    def setUp(self):
        self.suite = verify.Verification(
            argparse.Namespace(runtime="podman", cluster="existing", api_url="http://127.0.0.1:8080", timeout=1)
        )
        self.addCleanup(shutil.rmtree, self.suite.directory)
        self.addCleanup(self.suite.log.close)

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


if __name__ == "__main__":
    unittest.main()
