#!/usr/bin/env python3
"""Run the lecture03 deck on a disposable cluster, then delete the clean cluster."""

import argparse
import hashlib
import importlib.util
import json
import os
import re
import signal
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

# Reuse the existing bounded Compose transport and report format, not its exercises.
_spec = importlib.util.spec_from_file_location('verify_lab', Path(__file__).with_name('verify-lab.py'))
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
require = base.require
ROOT = base.ROOT
LAB = ROOT / 'kubernetes/lecture03'
SYSTEM_NAMESPACES = {'default', 'kube-system', 'kube-public', 'kube-node-lease', 'kube-flannel', 'yakd-dashboard'}
MYSQL = 'mysql-operator-26.10.0-2.3.1/'
KEDA = 'keda/upstream/keda-2.21.0.yaml'


def namespace_args(relative):
    # Operator bundles declare namespaces, sometimes several in the same file (KEDA).
    return () if relative.startswith(('autoscaler/', MYSQL, 'keda/upstream/')) else ('-n', 'default')


def keda_window(source, now):
    start = now.replace(second=0, microsecond=0) + timedelta(minutes=2)
    end = start + timedelta(minutes=2)
    for key, instant in [('start', start), ('end', end)]:
        source, count = re.subn(rf'(?m)^(\s*{key}:).*$', rf'\1 "{instant.minute} {instant.hour} * * *"', source)
        require(count == 1, f'Expected exactly one Cron {key}')
    return source, start, end


class Lecture03(base.Verification):
    def __init__(self, args):
        super().__init__(args)
        self.applied = []
        self.extras = []
        self.active = None
        self.operation_ids = set()

    def apply_file(self, relative, data=None, server_side=False):
        self.applied.append(relative)  # Register before apply: a multi-object apply can fail partially.
        self.k(*namespace_args(relative), 'apply', *(['--server-side'] if server_side else []), '-f', '-',
               data=data if data is not None else (LAB / relative).read_text(), timeout=300)

    def delete_file(self, relative):
        # MySQL needs its owned Secrets until its finalizers finish; foreground GC removes them too early.
        cascade = 'background' if relative == 'mycluster.yml' else 'foreground'
        self.k(*namespace_args(relative), 'delete', '--ignore-not-found', '--cascade=' + cascade,
               '--timeout=180s', '-f', '-', data=(LAB / relative).read_text(), timeout=210)
        self.applied.remove(relative)

    def rollout(self, deployment, namespace='default'):
        self.k('-n', namespace, 'rollout', 'status', 'deployment/' + deployment, '--timeout=5m', timeout=330)

    def replicas(self, name, count):
        deployment = self.obj('deployment', name, '-n', 'default')
        status = deployment.get('status', {})
        return (status.get('observedGeneration', 0) >= deployment['metadata']['generation']
                and deployment['spec'].get('replicas', 1) == count
                and status.get('replicas', 0) == count
                and status.get('availableReplicas', 0) == count)

    def snapshot(self, name, *args):
        data = self.obj(*args)
        (self.directory / (name + '.json')).write_text(json.dumps(data, indent=2))
        return data

    def preflight(self):
        require(self.args.recreate_empty_cluster,
                'Pass --recreate-empty-cluster to authorize replacing the disposable lecture03 cluster')
        for line in (LAB / 'SHA256SUMS').read_text().splitlines():
            digest, relative = line.split('  ', 1)
            require(hashlib.sha256((LAB / relative).read_bytes()).hexdigest() == digest,
                    'Manifest checksum mismatch: ' + relative)
        clusters = self.api('clusters')
        require(all(c['name'] == self.cluster for c in clusters), 'The provider contains another cluster; refusing changes')
        require(not self.api('volumes'), 'Existing cloud volumes; refusing changes')
        require(not any(o['status'] == 'RUNNING' for o in self.api('operations')), 'Cloud operations still running')
        if clusters:
            self.assert_clean()

    def assert_clean(self):
        require(not self.api('volumes'), 'Cloud volumes remain; inspect before continuing')
        namespaces = {n['metadata']['name'] for n in self.obj('namespaces')['items']}
        require(not namespaces - SYSTEM_NAMESPACES, f'Foreign namespaces: {namespaces - SYSTEM_NAMESPACES}')
        require(not self.obj('crd,pv')['items'], 'CRDs or persistent volumes exist; refusing cluster replacement')
        allowed = {('Service', 'kubernetes'), ('ConfigMap', 'kube-root-ca.crt'), ('ServiceAccount', 'default')}
        resources = self.obj('all,configmap,secret,pvc,serviceaccount,role,rolebinding,ingress,networkpolicy,pdb,resourcequota,limitrange',
                             '-n', 'default')['items']
        unexpected = [(r['kind'], r['metadata']['name']) for r in resources
                      if (r['kind'], r['metadata']['name']) not in allowed]
        require(not unexpected, f'Foreign or leftover default resources: {unexpected}')
        system_controllers = {
            ('Deployment', 'kube-system', 'coredns'), ('Deployment', 'kube-system', 'metrics-server'),
            ('Deployment', 'yakd-dashboard', 'yakd-dashboard'), ('DaemonSet', 'kube-system', 'kube-proxy'),
            ('DaemonSet', 'kube-flannel', 'kube-flannel-ds'),
        }
        controllers = self.obj('deployments,statefulsets,daemonsets,jobs,cronjobs,hpa,pvc', '-A')['items']
        foreign = [(r['kind'], r['metadata']['namespace'], r['metadata']['name']) for r in controllers
                   if (r['kind'], r['metadata']['namespace'], r['metadata']['name']) not in system_controllers]
        require(not foreign, f'Foreign or leftover controllers: {foreign}')
        # Detect installed controllers even when their workloads are in a system namespace.
        require(not self.k('get', 'apiservice', 'v1beta1.external.metrics.k8s.io', '--ignore-not-found', '-o', 'name'),
                'An external metrics adapter is already installed')
        for relative in ['autoscaler/cloud-config.yaml', 'autoscaler/rbac.yaml', 'autoscaler/deployment.yaml']:
            require(not self.k('get', '-f', '-', '--ignore-not-found', '-o', 'name', data=(LAB / relative).read_text()),
                    'Autoscaler resources already exist')
        require(not self.k('-n', 'kube-system', 'get', 'configmap/cluster-autoscaler-status',
                           'lease/cluster-autoscaler', '--ignore-not-found', '-o', 'name'),
                'Autoscaler generated resources remain')

    def configure_cluster(self, shape, minimum, maximum):
        require(self.args.recreate_empty_cluster, 'Missing --recreate-empty-cluster consent')
        clusters = self.api('clusters')
        require(all(c['name'] == self.cluster for c in clusters), 'The provider contains another cluster')
        if clusters:
            self.assert_clean()
            require(not any(o['status'] == 'RUNNING' for o in self.api('operations')), 'Cloud operations still running')
            c = clusters[0]
            require(c.get('status') == 'RUNNING', 'Cluster is not RUNNING; consult the operator')
            if (c.get('machineType'), c.get('minMachines'), c.get('maxMachines')) == (shape, minimum, maximum):
                self.wait('clean cluster Ready', lambda: len(self.ready_nodes()) == minimum)
                return
            require(self.args.recreate_empty_cluster, 'Missing --recreate-empty-cluster consent')
            self.cloud('clusters', 'delete', self.cluster, timeout=900)
        print(f'SETUP {shape}: min={minimum}, max={maximum}', flush=True)
        self.cloud('clusters', 'create', self.cluster, '--machine-type', shape,
                   '--min-nodes', str(minimum), '--max-nodes', str(maximum), timeout=1800)
        self.wait('new cluster Ready', lambda: len(self.ready_nodes()) == minimum)

    def delete_empty_cluster(self):
        require(self.args.recreate_empty_cluster, 'Missing --recreate-empty-cluster consent')
        self.assert_clean()
        require([c['name'] for c in self.api('clusters')] == [self.cluster],
                'Unexpected provider inventory; refusing final cluster deletion')
        self.cloud('clusters', 'delete', self.cluster, timeout=900)
        require(not self.api('clusters') and not self.api('volumes'), 'Cluster or cloud volumes remain')

    def hpa(self):
        self.apply_file('php-apache.yml')
        self.rollout('php-apache')
        self.apply_file('hpa.yml')
        self.k('-n', 'default', 'wait', '--for=condition=ScalingActive', 'hpa/php-apache', '--timeout=180s', timeout=210)
        self.wait('HPA initial single replica', lambda: self.replicas('php-apache', 1))
        self.snapshot('hpa-before', 'hpa', 'php-apache', '-n', 'default')
        self.extras.append('pod/load-generator')
        self.k('-n', 'default', 'run', 'load-generator', '--image=busybox:1.28', '--restart=Never',
               '--', '/bin/sh', '-c', 'while sleep 0.01; do wget -q -O- http://php-apache; done')
        self.wait('HPA more than one available replica', lambda:
                  self.obj('deployment', 'php-apache', '-n', 'default').get('status', {}).get('availableReplicas', 0) > 1)
        self.snapshot('hpa-loaded', 'hpa', 'php-apache', '-n', 'default')
        self.delete_extra('pod/load-generator')
        self.wait('HPA back to one replica (300s stabilization)', lambda: self.replicas('php-apache', 1))
        self.snapshot('hpa-after', 'hpa', 'php-apache', '-n', 'default')

    def ca_operations(self, kind):
        return [o for o in self.api('operations') if o['id'] not in self.operation_ids
                and o['type'] == kind and o['target'].startswith(self.cluster + '-m')
                and o['requester'] == 'cluster-autoscaler']

    def ca_down(self):
        self.k('-n', 'default', 'scale', 'deployment/lab-load', '--replicas=0')
        self.wait('CA back to one Ready node', lambda:
                  len(self.obj('nodes')['items']) == 1 and len(self.ready_nodes()) == 1)
        self.wait('provider worker teardown DONE', lambda:
                  not any(o['status'] == 'RUNNING' for o in self.api('operations'))
                  and len(self.api('machines')) == 1)
        adds = self.ca_operations('ADD_MACHINE')
        deletes = self.ca_operations('DELETE_MACHINE')
        require(all(o['status'] == 'DONE' for o in adds + deletes), 'CA operation failed; inspect provider report')
        require({o['target'] for o in adds} <= {o['target'] for o in deletes}, 'Missing worker deletion proof')

    def ca(self):
        self.operation_ids = {o['id'] for o in self.api('operations')}
        for name in ['cloud-config.yaml', 'rbac.yaml', 'deployment.yaml']:
            self.apply_file('autoscaler/' + name)
        self.rollout('cluster-autoscaler', 'kube-system')
        self.apply_file('load.yaml')
        self.wait('CA initial zero replicas', lambda: self.replicas('lab-load', 0))
        self.k('-n', 'default', 'scale', 'deployment/lab-load', '--replicas=4')
        self.wait('Pending / Insufficient cpu', lambda: any(
            e.get('reason') == 'FailedScheduling' and 'Insufficient cpu' in e.get('message', '')
            and e['involvedObject']['name'].startswith('lab-load-')
            for e in self.obj('events', '-n', 'default')['items']))
        self.wait('CA four available replicas and new nodes', lambda:
                  self.replicas('lab-load', 4) and len(self.ready_nodes()) >= 2)
        self.wait('provider ADD_MACHINE DONE', lambda:
                  bool(self.ca_operations('ADD_MACHINE')) and
                  all(o['status'] == 'DONE' for o in self.ca_operations('ADD_MACHINE')))
        self.snapshot('ca-up-nodes', 'nodes')
        self.snapshot('ca-up-pods', 'pods', '-n', 'default', '-l', 'app=lab-load')
        self.ca_down()
        (self.directory / 'ca-operations.json').write_text(json.dumps(
            self.ca_operations('ADD_MACHINE') + self.ca_operations('DELETE_MACHINE'), indent=2))
        self.snapshot('ca-down-nodes', 'nodes')

    def keda(self):
        self.apply_file(KEDA, server_side=True)
        for deployment in ['keda-operator', 'keda-metrics-apiserver', 'keda-admission']:
            self.rollout(deployment, 'keda')
        self.k('wait', '--for=condition=Available', 'apiservice/v1beta1.external.metrics.k8s.io', '--timeout=180s', timeout=210)
        self.snapshot('keda-components', 'pods', '-n', 'keda')
        self.apply_file('keda/deployment.yaml')
        self.rollout('keda-http')
        text, start, end = keda_window((LAB / 'keda/scaledobject.yaml').read_text(), datetime.now(timezone.utc))
        (self.directory / 'scaledobject.yaml').write_text(text)
        print(f'KEDA window: {start.isoformat()} -> {end.isoformat()}', flush=True)
        self.apply_file('keda/scaledobject.yaml', data=text)
        self.k('-n', 'default', 'wait', '--for=condition=Ready', 'scaledobject/keda-http-window', '--timeout=120s', timeout=150)
        self.wait('KEDA initial zero', lambda: self.replicas('keda-http', 0),
                  timeout=max(1, (start - datetime.now(timezone.utc)).total_seconds()))
        self.snapshot('keda-zero-before', 'deployment', 'keda-http', '-n', 'default')
        self.wait('KEDA two available replicas in window', lambda: self.replicas('keda-http', 2),
                  timeout=max(1, (end - datetime.now(timezone.utc)).total_seconds()))
        self.snapshot('keda-active', 'scaledobject', 'keda-http-window', '-n', 'default')
        scaled = self.obj('scaledobject', 'keda-http-window', '-n', 'default')
        hpas = self.obj('hpa', '-n', 'default')['items']
        require(any(any(owner['uid'] == scaled['metadata']['uid'] for owner in h['metadata'].get('ownerReferences', []))
                    for h in hpas), 'KEDA HPA does not belong to the ScaledObject')
        pod = self.obj('pods', '-n', 'default', '-l', 'app=keda-http')['items'][0]
        self.extras.append('pod/keda-client')
        self.k('-n', 'default', 'run', 'keda-client', '--image=busybox:1.28', '--restart=Never',
               '--', 'wget', '-qO-', 'http://' + pod['status']['podIP'] + ':5678')
        self.k('-n', 'default', 'wait', '--for=jsonpath={.status.phase}=Succeeded', 'pod/keda-client', '--timeout=90s', timeout=120)
        require(self.k('-n', 'default', 'logs', 'keda-client') == 'Hello, KEDA!', 'KEDA HTTP response mismatch')
        self.delete_extra('pod/keda-client')
        self.wait('KEDA returns to zero', lambda: self.replicas('keda-http', 0),
                  timeout=max(120, (end - datetime.now(timezone.utc)).total_seconds() + 120))
        self.snapshot('keda-zero-after', 'deployment', 'keda-http', '-n', 'default')
        require(len(self.ready_nodes()) == 1, 'KEDA exercise changed the number of nodes')

    def crd(self):
        self.apply_file('crontab-crd.yml')
        self.k('wait', '--for=condition=Established', 'crd/crontabs.stable.example.com', '--timeout=60s')
        self.apply_file('crontab-example.yml')
        custom = self.snapshot('crontab', 'crontabs', 'my-new-cron-object', '-n', 'default')
        require(custom['spec'] == {'cronSpec': '* * * * */5', 'image': 'my-awesome-cron-image'}, 'CronTab spec mismatch')
        require(not self.obj('pods', '-n', 'default')['items'], 'CRD exercise unexpectedly created a Pod')

    def mysql(self):
        self.apply_file(MYSQL + 'deploy-crds.yaml')
        for name in ['innodbclusters.mysql.oracle.com', 'mysqlbackups.mysql.oracle.com',
                     'mysqlclustersetfailovers.mysql.oracle.com',
                     'clusterkopfpeerings.zalando.org', 'kopfpeerings.zalando.org']:
            self.k('wait', '--for=condition=Established', 'crd/' + name, '--timeout=60s')
        self.apply_file(MYSQL + 'deploy-operator.yaml')
        self.rollout('mysql-operator', 'mysql-operator')
        self.extras.append('secret/mypwds')
        self.k('-n', 'default', 'create', 'secret', 'generic', 'mypwds', '--from-literal=rootUser=root',
               '--from-literal=rootHost=%', '--from-literal=rootPassword=cloud')
        self.apply_file('mycluster.yml')
        def online():
            state = self.obj('innodbcluster', 'mycluster', '-n', 'default').get('status', {}).get('cluster', {})
            return state.get('status') == 'ONLINE' and state.get('onlineInstances') == 3
        self.wait('MySQL three ONLINE members', online)
        self.rollout('mycluster-router')
        self.snapshot('mysql-online', 'innodbcluster', 'mycluster', '-n', 'default')
        self.snapshot('mysql-pvc', 'pvc', '-n', 'default')
        self.extras.append('pod/myshell')
        self.k('-n', 'default', 'run', 'myshell', '--restart=Never',
               '--image=container-registry.oracle.com/mysql/community-operator:26.10.0-2.3.1', '--command', '--', 'sleep', '3600')
        self.k('-n', 'default', 'wait', '--for=condition=Ready', 'pod/myshell', '--timeout=180s', timeout=210)
        sql = (LAB / 'verify-mysql.sql').read_text()
        result = self.k('-n', 'default', 'exec', '-i', 'myshell', '--', 'mysqlsh', '--sql',
                        '--uri=root@mycluster:3306', '--passwords-from-stdin', '--result-format=tabbed',
                        '-e', sql, data='cloud\n')
        (self.directory / 'mysql-query.txt').write_text(result)
        members = [line.split('\t') for line in result.splitlines() if '\tONLINE\t' in line]
        require(len(members) == 3 and sum(row[-1] == 'PRIMARY' for row in members) == 1,
                'SQL through Router did not return three ONLINE members and one PRIMARY')

    def delete_extra(self, name):
        self.k('-n', 'default', 'delete', name, '--ignore-not-found', '--timeout=180s', timeout=210)
        self.extras.remove(name)

    def cleanup_exercise(self):
        if self.active == 'ca' and 'load.yaml' in self.applied:
            if self.k('-n', 'default', 'get', 'deployment/lab-load', '--ignore-not-found', '-o', 'name'):
                self.ca_down()  # Keep the controller until the provider has finished deleting workers.
        for name in list(reversed(self.extras)):
            if name != 'secret/mypwds':
                self.delete_extra(name)
        if 'mycluster.yml' in self.applied:
            self.delete_file('mycluster.yml')
            self.k('-n', 'default', 'delete', 'pvc', *['datadir-mycluster-' + str(i) for i in range(3)],
                   '--ignore-not-found', '--timeout=180s', timeout=210)
            self.wait('MySQL PV and cloud storage released', lambda:
                      not self.api('volumes') and not self.obj('pv')['items'])
        for name in list(self.extras):
            self.delete_extra(name)
        for relative in list(reversed(self.applied)):
            self.delete_file(relative)
        if self.active == 'ca':
            self.k('-n', 'kube-system', 'delete', 'configmap/cluster-autoscaler-status',
                   'lease/cluster-autoscaler', '--ignore-not-found')
        self.assert_clean()

    def diagnostics(self):
        for label, args in [('nodes', ('nodes',)), ('pods', ('pods', '-A')), ('events', ('events', '-A'))]:
            try:
                self.snapshot('failure-' + label, *args)
            except Exception as exc:
                self.log.write(f'Diagnostics {label}: {exc}\n')
        for label, command in [('provider', self.compose + ['logs', '--tail=150', 'cloud'])]:
            try:
                (self.directory / ('failure-' + label + '.log')).write_text(self.run(command))
            except Exception as exc:
                self.log.write(f'Diagnostics {label}: {exc}\n')
        for namespace, deployment in [('kube-system', 'cluster-autoscaler'), ('keda', 'keda-operator'),
                                      ('mysql-operator', 'mysql-operator')]:
            try:
                (self.directory / ('failure-' + deployment + '.log')).write_text(
                    self.k('-n', namespace, 'logs', 'deployment/' + deployment, '--tail=150'))
            except Exception as exc:
                self.log.write(f'Diagnostics {deployment}: {exc}\n')
        self.log.flush()

    def execute(self):
        print(f'Lecture03: {self.cluster}; logs: {self.directory}', flush=True)
        try:
            if not self.check('preflight: disposable empty cluster and manifest checksums', self.preflight):
                return 1
            for name, shape, minimum, maximum in [
                ('hpa', 'lab-medium', 2, 2), ('ca', 'lab-small', 1, 3),
                ('keda', 'lab-small', 1, 3), ('crd', 'lab-small', 1, 3), ('mysql', 'lab-medium', 2, 2),
            ]:
                if not self.check('setup ' + name, lambda: self.configure_cluster(shape, minimum, maximum)):
                    self.diagnostics()
                    break
                self.active = name
                try:
                    passed = self.check(name, getattr(self, name))
                    if not passed:
                        self.diagnostics()
                finally:
                    cleaned = self.check('cleanup ' + name, self.cleanup_exercise)
                self.active = None
                if not cleaned:
                    self.diagnostics()
                if not passed or not cleaned:
                    break
            else:
                self.check('delete empty lecture03 cluster', self.delete_empty_cluster)
        except KeyboardInterrupt:
            self.results.append({'name': 'interrupted', 'status': 'FAIL', 'detail': 'Interrupted by operator'})
        finally:
            (self.directory / 'report.json').write_text(json.dumps(self.results, indent=2))
            self.log.close()
            print(f'Report: {self.directory / "report.json"}', flush=True)
        return 1 if any(r['status'] != 'PASS' for r in self.results) else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, epilog=(
        'Requires running cloud services. Recreates only an empty disposable lecture03 cluster; '
        'budget 30-60 minutes and capacity for two lab-medium nodes. On failure, inspect logs and consult the operator.'))
    parser.add_argument('--runtime', choices=('auto', 'docker', 'podman'), default=os.environ.get('CLOUD_RUNTIME', 'auto'))
    parser.add_argument('--recreate-empty-cluster', action='store_true',
                        help='Authorize deletion/recreation of the empty dedicated lecture03 cluster between exercises')
    parser.add_argument('--timeout', type=int, default=900, help='Polling timeout per checkpoint in seconds (default 900)')
    args = parser.parse_args()
    if not args.recreate_empty_cluster:
        parser.error('--recreate-empty-cluster is required; use only on the disposable course lab')
    if args.timeout < 1:
        parser.error('--timeout must be positive')
    args.cluster = 'lecture03'
    os.environ['PATH'] = str(Path.home() / '.local/bin') + os.pathsep + os.environ.get('PATH', '')
    try:
        detected = subprocess.run(['bash', str(ROOT / 'scripts/detect-runtime.sh'), args.runtime],
                                  capture_output=True, text=True, timeout=60, check=False)
        if detected.returncode:
            parser.error(detected.stderr.strip())
        args.runtime = detected.stdout.strip()
    except (OSError, subprocess.TimeoutExpired) as exc:
        parser.error(f'Runtime detection failed: {exc}')
    os.environ['CLOUD_RUNTIME'] = args.runtime
    def interrupt(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    return Lecture03(args).execute()


if __name__ == '__main__':
    sys.exit(main())
