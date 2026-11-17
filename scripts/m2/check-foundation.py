#!/usr/bin/env python3
"""Exercise storage protocols on a disposable PostgreSQL pod, separate from application data."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'
os.environ.setdefault('KUBECONFIG', str(Path.home() / '.kube' / 'aat-dev.yaml'))
KUBE = ['kubectl', '--context', 'aat-dev', '-n', NAMESPACE]


def main():
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True)
    run_id = uuid.uuid4().hex[:8]
    name = 'aat-foundation-' + run_id
    password = secrets.token_hex(32)
    databases = [f'aat_foundation_{run_id}_shard_{i}_test' for i in range(4)]
    private = STATE / f'foundation-{run_id}.json'
    container = None
    resources = [
        {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name},
         'stringData': {'password': password}},
        {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name, 'labels': {'role': 'store'}},
         'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                  'nodeSelector': {'aat.dev/control': 'true'},
                  'containers': [{'name': 'postgres', 'image': 'postgres:16.14-alpine',
                                  'env': [{'name': 'POSTGRES_PASSWORD', 'valueFrom': {'secretKeyRef': {'name': name, 'key': 'password'}}}],
                                  'resources': {'requests': {'memory': '512Mi'}, 'limits': {'memory': '1Gi'}},
                                  'readinessProbe': {'exec': {'command': ['pg_isready', '-U', 'postgres']}, 'periodSeconds': 2},
                                  'volumeMounts': [{'name': 'data', 'mountPath': '/var/lib/postgresql/data'}]}],
                  'volumes': [{'name': 'data', 'emptyDir': {}}]}},
    ]
    try:
        subprocess.run(KUBE + ['apply', '-f', '-'], input=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': resources}), text=True, check=True)
        subprocess.run(KUBE + ['wait', 'pod/' + name, '--for=condition=Ready', '--timeout=180s'], check=True)
        ip = subprocess.check_output(KUBE + ['get', 'pod', name, '-o', 'jsonpath={.status.podIP}'], text=True).strip()
        for database in databases:
            subprocess.run(KUBE + ['exec', name, '--', 'psql', '-U', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', f'CREATE DATABASE {database}'], check=True)
        private.write_text(json.dumps([f'postgresql://postgres:{password}@{ip}:5432/{database}?sslmode=disable&connect_timeout=2' for database in databases]))
        container = subprocess.check_output(['sudo', 'buildah', 'from', 'golang:1.23-alpine'], text=True).strip()
        subprocess.run([
            'sudo', 'buildah', 'run', '--isolation', 'chroot', '--network', 'host', '--workingdir', '/workspace',
            '--volume', f'{ROOT}:/workspace:ro', '--volume', f'{STATE}/module-cache:/go/pkg/mod',
            '--volume', f'{STATE}/build-cache:/root/.cache/go-build', '--volume', f'{private}:/run/foundation-dsns.json:ro',
            '--env', 'AAT_FOUNDATION_DSN_FILE=/run/foundation-dsns.json', container, '--',
            'go', 'test', '-p', '1', '-v', '-count=1', '-timeout=60s',
            './services/aggregator/internal/foundationtest', './services/aggregator/internal/sharded',
        ], check=True)
    finally:
        failures = []
        if container and subprocess.run(['sudo', 'buildah', 'rm', container], stdout=subprocess.DEVNULL).returncode:
            failures.append('temporary Go container')
        private.unlink(missing_ok=True)
        if subprocess.run(KUBE + ['delete', 'pod/' + name, 'secret/' + name, '--ignore-not-found=true', '--wait=true'], stdout=subprocess.DEVNULL).returncode:
            failures.append('temporary PostgreSQL pod/Secret')
        if failures:
            message = 'Cleanup failed for: ' + ', '.join(failures)
            if sys.exc_info()[0] is not None:
                print(message, file=sys.stderr)
            else:
                raise RuntimeError(message)


if __name__ == '__main__':
    main()
