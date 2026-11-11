#!/usr/bin/env python3
"""Print a fresh VPA TLS Secret manifest; pipe stdout to kubectl apply -f -."""

import base64
import json
import subprocess
import sys
import tempfile
from pathlib import Path


def generate_secret():
    # Unlike the upstream helper, use a private temporary directory and never leave keys on disk.
    with tempfile.TemporaryDirectory(prefix='lecture03-vpa-tls-') as directory:
        root = Path(directory)
        config = root / 'openssl.cnf'
        config.write_text('''[req]
distinguished_name = dn
[dn]
[ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
[server]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:vpa-webhook.kube-system.svc
''')
        commands = [
            ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'caKey.pem',
             '-out', 'caCert.pem', '-days', '7', '-subj', '/CN=lecture03-vpa-ca',
             '-config', str(config), '-extensions', 'ca'],
            ['req', '-new', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'serverKey.pem',
             '-out', 'server.csr', '-subj', '/CN=vpa-webhook.kube-system.svc', '-config', str(config)],
            ['x509', '-req', '-in', 'server.csr', '-CA', 'caCert.pem', '-CAkey', 'caKey.pem',
             '-CAcreateserial', '-out', 'serverCert.pem', '-days', '7', '-extfile', str(config),
             '-extensions', 'server'],
        ]
        for command in commands:
            subprocess.run(['openssl', *command], cwd=root, check=True, capture_output=True, timeout=30)
        return {
            'apiVersion': 'v1', 'kind': 'Secret', 'type': 'Opaque',
            'metadata': {'name': 'vpa-tls-certs', 'namespace': 'kube-system'},
            'data': {name: base64.b64encode((root / name).read_bytes()).decode('ascii')
                     for name in ('caKey.pem', 'caCert.pem', 'serverKey.pem', 'serverCert.pem')},
        }


if __name__ == '__main__':
    json.dump(generate_secret(), sys.stdout)
    print()
