"""Offline consistency checks; no connection to a cluster or Docker is made."""
from pathlib import Path
import subprocess
import yaml

root = Path(__file__).resolve().parent
resources = {}
count = 0
for path in sorted(root.rglob('*.yaml')):
    for obj in yaml.safe_load_all(path.read_text()):
        assert obj and obj.get('apiVersion') and obj.get('kind'), path
        meta = obj['metadata']
        assert obj['kind'] != 'Namespace', path
        assert meta.get('namespace') == 'default', (path, meta)
        key = (meta['namespace'], obj['kind'], meta['name'])
        # vote-update.yaml intentionally replaces one Deployment's desired state.
        if path.name != 'vote-update.yaml':
            assert key not in resources, ('duplicate', key)
            resources[key] = obj
        count += 1
        if obj['kind'] in ('Deployment', 'DaemonSet'):
            assert obj['apiVersion'] == 'apps/v1', key
            spec = obj['spec']
            labels = spec['template']['metadata']['labels']
            assert labels.get('course-lab') == 'lecture02', key
            assert all(labels.get(k) == v for k,v in spec['selector']['matchLabels'].items()), key
            assert spec['template']['spec']['containers'], key
            if obj['kind'] == 'DaemonSet':
                assert 'replicas' not in spec, key

for (namespace,kind,name),obj in resources.items():
    if kind in ('Deployment', 'DaemonSet'):
        pod = obj['spec']['template']['spec']
        volumes = {v['name']:v for v in pod.get('volumes',[])}
        for v in volumes.values():
            if 'persistentVolumeClaim' in v:
                assert (namespace,'PersistentVolumeClaim',v['persistentVolumeClaim']['claimName']) in resources, (name,v)
            if 'configMap' in v:
                assert (namespace,'ConfigMap',v['configMap']['name']) in resources, (name,v)
        for c in pod['containers']:
            assert c['image'] and c['name'] and c['resources']['requests'], name
            for mount in c.get('volumeMounts',[]):
                assert mount['name'] in volumes, (name,mount)
            for env in c.get('envFrom',[]):
                assert (namespace,'Secret',env['secretRef']['name']) in resources, (name,env)
    if kind == 'Service':
        spec = obj['spec']
        targets = [d for (n,k,_),d in resources.items() if n==namespace and k=='Deployment' and
                   all(d['spec']['template']['metadata']['labels'].get(a)==b for a,b in spec['selector'].items())]
        assert len(targets) == 1, ('Service must select exactly one Deployment', name)
        for port in spec['ports']:
            assert all(any(cp['containerPort']==port['targetPort']
                           for c in d['spec']['template']['spec']['containers'] for cp in c.get('ports',[]))
                       for d in targets), ('targetPort mismatch',name)
    if kind == 'PersistentVolumeClaim':
        assert obj['spec']['storageClassName']=='cloud-retain', name

network = list(yaml.safe_load_all((root/'ex03/network.yaml').read_text()))
placements = [x['spec']['template']['spec']['nodeSelector']['lecture02-role'] for x in network if x['kind']=='Deployment']
assert set(placements)=={'backend','frontend'}
roll = resources[('default','Deployment','rollout')]
assert roll['spec']['replicas']==4 and roll['spec']['strategy']['rollingUpdate']=={'maxSurge':0,'maxUnavailable':2}
assert roll['spec']['minReadySeconds']==10
for name in ('mariadb', 'db'):
    assert resources[('default','Deployment',name)]['spec']['strategy']['type']=='Recreate'
assert resources[('default','Deployment','adminer')]['spec']['template']['spec']['containers'][0]['env'][0]['value'] == 'mariadb'
assert {name for (ns,kind,name),obj in resources.items() if kind=='Deployment' and obj['metadata'].get('labels',{}).get('app.kubernetes.io/part-of')=='voting'} == {'db','redis','vote','result','worker'}
for path in root.glob('*.yml'):
    for obj in yaml.safe_load_all(path.read_text()):
        assert obj['metadata']['namespace'] == 'default', path

helper = root/'ex03/prepare.sh'
for shell in ('bash','zsh'):
    subprocess.run([shell,'-n',str(helper)],check=True)
    # The helper must label both discovered workers and refuse a one-worker cluster.
    for nodes,success in [('worker-a\\nworker-b\\n',True),('worker-a\\n',False)]:
        script = 'kubectl() { if [ "$1" = get ]; then printf "'+nodes+'"; else printf "LABEL %s\\n" "$*"; fi; }; source "$1"'
        result = subprocess.run([shell,'-c',script,'helper-check',str(helper)],capture_output=True,text=True)
        assert (result.returncode==0)==success, (shell,result.stderr)
        if success:assert result.stdout.count('LABEL ')==2, result.stdout
        else:assert 'LABEL ' not in result.stdout, result.stdout
for path in root.rglob('*'):
    if path.is_file() and 'source-evidence' not in path.parts and path.suffix in ('.yaml','.sh'):
        assert 'lab06' not in path.read_text(), path
print(f'PASS: {count} YAML objects; selectors, Service ports, configuration, PVC references, strategies and Bash/zsh helper checks.')
print('OFFLINE ONLY: this script does not run Kubernetes API validation or live exercises.')
