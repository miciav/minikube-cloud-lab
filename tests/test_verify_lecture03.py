"""Safety and result checks for the lecture03 runner; no cluster required."""
import argparse
import base64
import subprocess
import tempfile
import importlib.util
import json
import shutil
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    'verify_lecture03', Path(__file__).resolve().parents[1] / 'scripts/verify-lecture03.py')
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class Lecture03Test(unittest.TestCase):
    def setUp(self):
        self.suite = verify.Lecture03(argparse.Namespace(
            runtime='docker', cluster='lecture03', timeout=1, recreate_empty_cluster=True))
        self.addCleanup(shutil.rmtree, self.suite.directory)
        self.addCleanup(self.suite.log.close)

    def test_generated_vpa_tls_certificate_has_valid_ca_and_service_hostname(self):
        spec = importlib.util.spec_from_file_location('vpa_tls', verify.LAB / 'vpa/generate-tls.py')
        tls = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(tls)
        secret = tls.generate_secret()
        self.assertEqual(secret['metadata'], {'name': 'vpa-tls-certs', 'namespace': 'kube-system'})
        self.assertEqual(set(secret['data']), {'caKey.pem', 'caCert.pem', 'serverKey.pem', 'serverCert.pem'})
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ['caCert.pem', 'serverCert.pem']:
                (root / name).write_bytes(base64.b64decode(secret['data'][name]))
            subprocess.run(['openssl', 'verify', '-CAfile', str(root / 'caCert.pem'), str(root / 'serverCert.pem')],
                           check=True, capture_output=True)
            subprocess.run(['openssl', 'x509', '-in', str(root / 'serverCert.pem'),
                            '-checkhost', 'vpa-webhook.kube-system.svc', '-noout'], check=True, capture_output=True)

    def test_recreation_requires_explicit_consent(self):
        self.suite.args.recreate_empty_cluster = False
        self.suite.api = Mock()
        self.suite.cloud = Mock()
        with self.assertRaisesRegex(RuntimeError, 'recreate-empty-cluster'):
            self.suite.preflight()
        self.suite.cloud.assert_not_called()
        self.suite.api.assert_not_called()

    def test_foreign_cluster_never_deleted(self):
        self.suite.api = Mock(return_value=[{'name': 'production'}])
        self.suite.cloud = Mock()
        with self.assertRaisesRegex(RuntimeError, 'another cluster'):
            self.suite.configure_cluster('lab-small', 1, 3)
        self.suite.cloud.assert_not_called()

    def test_dirty_cluster_never_deleted(self):
        self.suite.api = Mock(return_value=[{'name': 'lecture03'}])
        self.suite.assert_clean = Mock(side_effect=RuntimeError('foreign workload'))
        self.suite.cloud = Mock()
        with self.assertRaisesRegex(RuntimeError, 'foreign workload'):
            self.suite.configure_cluster('lab-medium', 2, 2)
        self.suite.cloud.assert_not_called()

    def test_foreign_default_resource_rejected(self):
        def obj(*args):
            if args[0] == 'namespaces':
                return {'items': [{'metadata': {'name': n}} for n in verify.SYSTEM_NAMESPACES]}
            if args[0] == 'crd,pv':
                return {'items': []}
            return {'items': [{'kind': 'Secret', 'metadata': {'name': 'customer-data'}}]}
        self.suite.api = Mock(return_value=[])
        self.suite.obj = obj
        with self.assertRaisesRegex(RuntimeError, 'customer-data'):
            self.suite.assert_clean()

    def test_cli_without_consent_does_not_detect_runtime(self):
        with patch.object(verify.sys, 'argv', ['verify-lecture03.py']), patch.object(verify.subprocess, 'run') as run:
            with self.assertRaises(SystemExit) as exc:
                verify.main()
            self.assertEqual(exc.exception.code, 2)
            run.assert_not_called()

    def test_unknown_namespace_blocks_recreation(self):
        self.suite.api = Mock(return_value=[])
        self.suite.obj = Mock(return_value={'items': [{'metadata': {'name': 'student-data'}}]})
        with self.assertRaisesRegex(RuntimeError, 'student-data'):
            self.suite.assert_clean()

    def test_existing_volumes_block_recreation(self):
        self.suite.api = Mock(return_value=[{'id': 'foreign-volume'}])
        self.suite.k = Mock()
        with self.assertRaisesRegex(RuntimeError, 'volumes remain'):
            self.suite.assert_clean()
        self.suite.k.assert_not_called()

    def test_interrupt_still_cleans_active_exercise(self):
        self.suite.preflight = Mock()
        self.suite.configure_cluster = Mock()
        self.suite.hpa = Mock(side_effect=KeyboardInterrupt)
        self.suite.cleanup_exercise = Mock()
        self.suite.ca = Mock()
        self.assertEqual(self.suite.execute(), 1)
        self.suite.cleanup_exercise.assert_called_once()
        self.suite.ca.assert_not_called()

    def test_partial_apply_cleanup_removes_only_registered_files(self):
        self.suite.active = 'hpa'
        self.suite.applied = ['php-apache.yml']
        self.suite.k = Mock(return_value='')
        self.suite.assert_clean = Mock()
        self.suite.cleanup_exercise()
        self.assertEqual(self.suite.applied, [])
        self.suite.k.assert_called_once()
        self.assertIn('--cascade=foreground', self.suite.k.call_args.args)
        self.assertEqual(self.suite.k.call_args.kwargs['data'], (verify.LAB / 'php-apache.yml').read_text())

    def test_partial_apply_is_registered_before_command(self):
        self.suite.k = Mock(side_effect=RuntimeError('partial apply'))
        with self.assertRaisesRegex(RuntimeError, 'partial apply'):
            self.suite.apply_file('php-apache.yml')
        self.assertEqual(self.suite.applied, ['php-apache.yml'])

    def test_apply_uses_manifest_stdin_and_explicit_namespace(self):
        self.suite.k = Mock(return_value='created')
        self.suite.apply_file('load.yaml')
        self.assertEqual(self.suite.k.call_args.args, ('-n', 'default', 'apply', '-f', '-'))
        self.assertIn('replicas: 0', self.suite.k.call_args.kwargs['data'])

    def test_mysql_cleanup_leaves_dependency_order_to_operator(self):
        self.suite.applied = ['mycluster.yml']
        self.suite.k = Mock(return_value='')
        self.suite.delete_file('mycluster.yml')
        self.assertIn('--cascade=background', self.suite.k.call_args.args)
        self.assertNotIn('--cascade=foreground', self.suite.k.call_args.args)
        self.assertEqual(self.suite.applied, [])

    def test_operator_manifests_keep_their_own_namespaces(self):
        for relative in ['autoscaler/cloud-config.yaml', verify.KEDA, verify.MYSQL + 'deploy-operator.yaml', 'vpa/upstream/recommender-deployment.yaml']:
            with self.subTest(relative=relative):
                self.suite.k = Mock(return_value='')
                self.suite.apply_file(relative)
                self.assertNotIn('-n', self.suite.k.call_args.args)
                self.suite.delete_file(relative)
                self.assertNotIn('-n', self.suite.k.call_args.args)

    def test_window_rolls_over_midnight_without_editing_source(self):
        source = (verify.LAB / 'keda/scaledobject.yaml').read_text()
        text, start, end = verify.keda_window(source, datetime(2026, 12, 31, 23, 59, 45, tzinfo=timezone.utc))
        self.assertEqual(start.isoformat(), '2027-01-01T00:01:00+00:00')
        self.assertEqual(end.isoformat(), '2027-01-01T00:03:00+00:00')
        self.assertIn('start: "1 0 * * *"', text)
        self.assertIn('end: "3 0 * * *"', text)
        self.assertEqual((verify.LAB / 'keda/scaledobject.yaml').read_text(), source)

    def test_replica_check_rejects_stale_status_and_remaining_replicas(self):
        deployment = {'metadata': {'generation': 2}, 'spec': {'replicas': 0},
                      'status': {'observedGeneration': 1, 'replicas': 0}}
        self.suite.obj = Mock(return_value=deployment)
        self.assertFalse(self.suite.replicas('load', 0))
        deployment['status']['observedGeneration'] = 2
        deployment['status']['replicas'] = 1
        self.assertFalse(self.suite.replicas('load', 0))
        deployment['status']['replicas'] = 0
        self.assertTrue(self.suite.replicas('load', 0))

    def test_ca_cleanup_keeps_controller_if_provider_has_not_converged(self):
        self.suite.active = 'ca'
        self.suite.applied = ['autoscaler/deployment.yaml', 'load.yaml']
        self.suite.k = Mock(return_value='deployment.apps/lab-load')
        self.suite.ca_down = Mock(side_effect=RuntimeError('worker deletion incomplete'))
        self.suite.delete_file = Mock()
        with self.assertRaisesRegex(RuntimeError, 'worker deletion incomplete'):
            self.suite.cleanup_exercise()
        self.suite.delete_file.assert_not_called()

    def test_provider_proof_ignores_old_and_foreign_operations(self):
        self.suite.operation_ids = {'old'}
        self.suite.api = Mock(return_value=[
            {'id': 'old', 'type': 'ADD_MACHINE', 'target': 'lecture03-m02', 'requester': 'cluster-autoscaler', 'status': 'DONE'},
            {'id': 'foreign', 'type': 'ADD_MACHINE', 'target': 'another-m02', 'requester': 'cluster-autoscaler', 'status': 'DONE'}])
        self.assertEqual(self.suite.ca_operations('ADD_MACHINE'), [])

    def test_success_runs_deck_order_and_deletes_empty_cluster(self):
        order = []
        self.suite.preflight = Mock()
        self.suite.configure_cluster = Mock()
        self.suite.assert_clean = Mock()
        for name in ('hpa', 'vpa', 'ca', 'keda', 'crd', 'mysql'):
            setattr(self.suite, name, Mock(side_effect=lambda name=name: order.append(name)))
        self.suite.cleanup_exercise = Mock(side_effect=lambda: order.append('cleanup ' + self.suite.active))
        self.suite.delete_empty_cluster = Mock()
        self.assertEqual(self.suite.execute(), 0)
        self.assertEqual(order, ['hpa', 'cleanup hpa', 'vpa', 'cleanup vpa', 'ca', 'cleanup ca', 'keda', 'cleanup keda',
                                 'crd', 'cleanup crd', 'mysql', 'cleanup mysql'])
        self.assertEqual(self.suite.configure_cluster.call_count, 6)
        self.suite.delete_empty_cluster.assert_called_once()

    def test_final_deletion_requires_clean_cluster(self):
        self.suite.assert_clean = Mock(side_effect=RuntimeError('volumes remain'))
        self.suite.cloud = Mock()
        with self.assertRaisesRegex(RuntimeError, 'volumes remain'):
            self.suite.delete_empty_cluster()
        self.suite.cloud.assert_not_called()

    def test_final_deletion_checks_provider_is_empty(self):
        self.suite.assert_clean = Mock()
        self.suite.cloud = Mock()
        self.suite.api = Mock(side_effect=[[{'name': 'lecture03'}], [], []])
        self.suite.delete_empty_cluster()
        self.suite.cloud.assert_called_once_with('clusters', 'delete', 'lecture03', timeout=900)

    def test_vpa_requires_new_uids_and_changed_cpu_with_unchanged_limits(self):
        pod = {'metadata': {'uid': 'new'}, 'spec': {'containers': [{'name': 'worker', 'resources': {
            'requests': {'cpu': '500m', 'memory': '16Mi'}, 'limits': {'cpu': '500m', 'memory': '64Mi'}}}]},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        self.assertTrue(verify.vpa_pod_updated(pod, {'old'}))
        self.assertFalse(verify.vpa_pod_updated(pod, {'new'}))
        pod['spec']['containers'][0]['resources']['requests']['cpu'] = '50m'
        self.assertFalse(verify.vpa_pod_updated(pod, {'old'}))
        pod['spec']['containers'][0]['resources']['requests']['cpu'] = '0.5'
        self.assertTrue(verify.vpa_pod_updated(pod, {'old'}))
        pod['spec']['containers'][0]['resources']['limits']['cpu'] = '1'
        self.assertFalse(verify.vpa_pod_updated(pod, {'old'}))

    def test_failure_cleans_active_exercise_and_stops(self):
        self.suite.preflight = Mock()
        self.suite.configure_cluster = Mock()
        self.suite.assert_clean = Mock()
        self.suite.hpa = Mock(side_effect=RuntimeError('load failed'))
        self.suite.ca = Mock()
        self.suite.cleanup_exercise = Mock()
        self.suite.diagnostics = Mock()
        self.assertEqual(self.suite.execute(), 1)
        self.suite.ca.assert_not_called()
        self.suite.cleanup_exercise.assert_called_once()
        self.suite.configure_cluster.assert_called_once_with('lab-medium', 2, 2)
        report=json.loads((self.suite.directory/'report.json').read_text())
        self.assertTrue(any(r['status']=='FAIL' for r in report))

    def test_cleanup_failure_stops_before_next_cluster(self):
        self.suite.preflight = Mock()
        self.suite.configure_cluster = Mock()
        self.suite.assert_clean = Mock()
        self.suite.hpa = Mock()
        self.suite.ca = Mock()
        self.suite.cleanup_exercise = Mock(side_effect=RuntimeError('stuck resource'))
        self.suite.diagnostics = Mock()
        self.assertEqual(self.suite.execute(), 1)
        self.suite.ca.assert_not_called()
        self.suite.configure_cluster.assert_called_once()
        self.suite.diagnostics.assert_called_once()


if __name__ == '__main__':
    unittest.main()
